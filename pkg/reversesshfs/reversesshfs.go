package reversesshfs

import (
	"bytes"
	"html/template"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/AkihiroSuda/sshocker/pkg/ssh"
	"github.com/AkihiroSuda/sshocker/pkg/util"
	"github.com/pkg/errors"
	"github.com/pkg/sftp"
	"github.com/sirupsen/logrus"
)

type ReverseSSHFS struct {
	*ssh.SSHConfig
	LocalPath           string
	Host                string
	Port                int
	RemotePath          string
	Readonly            bool
	sshCmd              *exec.Cmd
	SSHFSAdditionalArgs []string
}

func (rsf *ReverseSSHFS) Prepare() error {
	sshBinary := rsf.SSHConfig.Binary()
	sshArgs := rsf.SSHConfig.Args()
	if !filepath.IsAbs(rsf.RemotePath) {
		return errors.Errorf("unexpected relative path: %q", rsf.RemotePath)
	}
	sshArgs = append(sshArgs, "-p", strconv.Itoa(rsf.Port), rsf.Host, "--", "mkdir", "-p", rsf.RemotePath)
	sshCmd := exec.Command(sshBinary, sshArgs...)
	logrus.Debugf("executing ssh for preparing sshfs: %s %v", sshCmd.Path, sshCmd.Args)
	out, err := sshCmd.CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to mkdir %q (remote): %q", rsf.RemotePath, string(out))
	}
	return nil
}

func (rsf *ReverseSSHFS) Start() error {
	sshBinary := rsf.SSHConfig.Binary()
	sshArgs := rsf.SSHConfig.Args()
	if !filepath.IsAbs(rsf.LocalPath) {
		return errors.Errorf("unexpected relative path: %q", rsf.LocalPath)
	}
	if !filepath.IsAbs(rsf.RemotePath) {
		return errors.Errorf("unexpected relative path: %q", rsf.RemotePath)
	}
	sshArgs = append(sshArgs, "-p", strconv.Itoa(rsf.Port), rsf.Host, "--", "sshfs", ":"+rsf.LocalPath, rsf.RemotePath, "-o", "slave")
	if rsf.Readonly {
		sshArgs = append(sshArgs, "-o", "ro")
	}
	sshArgs = append(sshArgs, rsf.SSHFSAdditionalArgs...)
	rsf.sshCmd = exec.Command(sshBinary, sshArgs...)
	rsf.sshCmd.Stderr = os.Stderr
	stdinPipe, err := rsf.sshCmd.StdinPipe()
	if err != nil {
		return err
	}
	stdoutPipe, err := rsf.sshCmd.StdoutPipe()
	if err != nil {
		return err
	}
	stdio := &util.RWC{
		ReadCloser:  stdoutPipe,
		WriteCloser: stdinPipe,
	}
	var sftpOpts []sftp.ServerOption
	if rsf.Readonly {
		sftpOpts = append(sftpOpts, sftp.ReadOnly())
	}
	// NOTE: sftp.NewServer doesn't support specifying the root.
	// https://github.com/pkg/sftp/pull/238
	//
	// TODO: use sftp.NewRequestServer with custom handlers to mitigate potential vulnerabilities.
	server, err := sftp.NewServer(stdio, sftpOpts...)
	if err != nil {
		return err
	}
	logrus.Debugf("executing ssh for remote sshfs: %s %v", rsf.sshCmd.Path, rsf.sshCmd.Args)
	if err := rsf.sshCmd.Start(); err != nil {
		return err
	}
	logrus.Debugf("starting sftp server for %v", rsf.LocalPath)
	go func() {
		if srvErr := server.Serve(); srvErr != nil {
			if errors.Is(srvErr, io.EOF) {
				logrus.WithError(srvErr).Debugf("sftp server for %v exited with EOF (negligible)", rsf.LocalPath)
			} else {
				logrus.WithError(srvErr).Errorf("sftp server for %v exited", rsf.LocalPath)
			}
		}
	}()
	if err := rsf.waitForRemoteReady(); err != nil {
		// not a fatal error
		logrus.WithError(err).Warnf("failed to confirm whether %v [remote] is successfully mounted", rsf.RemotePath)
	}
	return nil
}

func (rsf *ReverseSSHFS) waitForRemoteReady() error {
	scriptName := "wait-for-remote-ready"
	scriptTemplate := `#!/bin/sh
set -eu
dir="{{.Dir}}"
max_trial="{{.MaxTrial}}"
LANG=C
LC_ALL=C
export LANG LC_ALL
i=0
while : ; do
  # FIXME: not really robust
  if mount | grep "on ${dir}" | egrep -qw "fuse.sshfs|osxfuse"; then
    echo '{"return":{}}'
    exit 0
  fi
  sleep 1
  if [ $i -ge ${max_trial} ]; then
    echo >&2 "sshfs does not seem to be mounted on ${dir}"
    exit 1
  fi
  i=$((i + 1))
done
`
	t, err := template.New(scriptName).Parse(scriptTemplate)
	if err != nil {
		return err
	}
	m := map[string]string{
		// rsf.RemotePath should have been verified during rsf.Prepare()
		"Dir":      rsf.RemotePath,
		"MaxTrial": "30",
	}
	var b bytes.Buffer
	if err := t.Execute(&b, m); err != nil {
		return err
	}
	script := b.String()
	logrus.Debugf("generated script %q with map %v: %q", scriptName, m, script)
	stdout, stderr, err := ssh.ExecuteScript(rsf.Host, rsf.Port, rsf.SSHConfig, script, scriptName)
	logrus.Debugf("executed script %q, stdout=%q, stderr=%q, err=%v", scriptName, stdout, stderr, err)
	return err
}

func (rsf *ReverseSSHFS) Close() error {
	logrus.Debugf("killing ssh server for remote sshfs: %s %v", rsf.sshCmd.Path, rsf.sshCmd.Args)
	if err := rsf.sshCmd.Process.Kill(); err != nil {
		return err
	}
	return nil
}
