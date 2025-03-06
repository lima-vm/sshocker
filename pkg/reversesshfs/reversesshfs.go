package reversesshfs

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/lima-vm/sshocker/pkg/ssh"
	"github.com/lima-vm/sshocker/pkg/util"
	"github.com/pkg/sftp"
	"github.com/sirupsen/logrus"
)

type Driver = string

const (
	DriverAuto              = Driver("auto")                // Default
	DriverBuiltin           = Driver("builtin")             // Legacy. Unrecommended.
	DriverOpensshSftpServer = Driver("openssh-sftp-server") // More robust and secure. Recommended.
)

type ReverseSSHFS struct {
	*ssh.SSHConfig
	Driver                  Driver
	OpensshSftpServerBinary string // used only when Driver == DriverOpensshSftpServer
	LocalPath               string
	Host                    string
	Port                    int
	RemotePath              string
	Readonly                bool
	sshCmd                  *exec.Cmd
	opensshSftpServerCmd    *exec.Cmd
	SSHFSAdditionalArgs     []string
}

func (rsf *ReverseSSHFS) Prepare() error {
	sshBinary := rsf.SSHConfig.Binary()
	sshArgs := rsf.SSHConfig.Args()
	if !path.IsAbs(rsf.RemotePath) {
		return fmt.Errorf("unexpected relative path: %q", rsf.RemotePath)
	}
	if rsf.Port != 0 {
		sshArgs = append(sshArgs, "-p", strconv.Itoa(rsf.Port))
	}
	sshArgs = append(sshArgs, rsf.Host, "--")
	sshArgs = append(sshArgs, "mkdir", "-p", strconv.Quote(rsf.RemotePath))
	sshCmd := exec.Command(sshBinary, sshArgs...)
	logrus.Debugf("executing ssh for preparing sshfs: %s %v", sshCmd.Path, sshCmd.Args)
	out, err := sshCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to mkdir %q (remote): %q: %w", rsf.RemotePath, string(out), err)
	}
	return nil
}

func DetectOpensshSftpServerBinary() string {
	if exe, err := exec.LookPath("sftp-server"); err == nil {
		return exe
	}
	homebrewSSHD := []string{
		"/usr/local/sbin/sshd",
		"/opt/homebrew/sbin/sshd",
	}
	for _, f := range homebrewSSHD {
		// sshd is like "/usr/local/Cellar/openssh/8.9p1/sbin/sshd"
		sshd, err := filepath.EvalSymlinks(f)
		if err != nil {
			continue
		}
		// local is like "/usr/local/Cellar/openssh"
		local := filepath.Dir(filepath.Dir(sshd))
		// sftpServer is like "/usr/local/Cellar/openssh/8.9p1/libexec/sftp-server"
		sftpServer := filepath.Join(local, "libexec", "sftp-server")
		if exe, err := exec.LookPath(sftpServer); err == nil {
			return exe
		}
	}
	if runtime.GOOS == "windows" {
		// unix path is like "/usr/lib/ssh/sftp-server"
		cygpathCmd := exec.Command("cygpath", "-w", "/usr/lib/ssh/sftp-server")
		// windows path is like `C:\msys64\usr\lib\ssh\sftp-server.exe`
		if out, err := cygpathCmd.Output(); err == nil {
			sftpServer := strings.TrimSpace(string(out))
			if exe, err := exec.LookPath(sftpServer); err == nil {
				return exe
			}
		}
	}
	candidates := []string{
		"/usr/libexec/sftp-server",         // macOS, OpenWrt
		"/usr/libexec/openssh/sftp-server", // Fedora
		"/usr/lib/sftp-server",             // Debian (symlink to openssh/sftp-server)
		"/usr/lib/openssh/sftp-server",     // Debian
		"/usr/lib/ssh/sftp-server",         // Alpine
	}
	for _, cand := range candidates {
		if exe, err := exec.LookPath(cand); err == nil {
			return exe
		}
	}
	return ""
}

func DetectDriver(explicitOpensshSftpServerBinary string) (Driver, string, error) {
	if explicitOpensshSftpServerBinary != "" {
		exe, err := exec.LookPath(explicitOpensshSftpServerBinary)
		if err != nil {
			return "", "", err
		}
		return DriverOpensshSftpServer, exe, nil
	}
	exe := DetectOpensshSftpServerBinary()
	if exe != "" {
		return DriverOpensshSftpServer, exe, nil
	}
	return DriverBuiltin, "", nil
}

func (rsf *ReverseSSHFS) Start() error {
	sshBinary := rsf.SSHConfig.Binary()
	sshArgs := rsf.SSHConfig.Args()
	if !filepath.IsAbs(rsf.LocalPath) && !path.IsAbs(rsf.LocalPath) {
		return fmt.Errorf("unexpected relative path: %q", rsf.LocalPath)
	}
	if runtime.GOOS == "windows" && path.IsAbs(rsf.LocalPath) {
		logrus.Infof("Accepting %q Unix path, assuming Cygwin/msys2 OpenSSH", rsf.LocalPath)
	}
	if !path.IsAbs(rsf.RemotePath) {
		return fmt.Errorf("unexpected relative path: %q", rsf.RemotePath)
	}
	if rsf.Port != 0 {
		sshArgs = append(sshArgs, "-p", strconv.Itoa(rsf.Port))
	}
	sshArgs = append(sshArgs, rsf.Host, "--")
	sshArgs = append(sshArgs, "sshfs", strconv.Quote(":"+rsf.LocalPath), strconv.Quote(rsf.RemotePath), "-o", "slave")
	if rsf.Readonly {
		sshArgs = append(sshArgs, "-o", "ro")
	}
	sshArgs = append(sshArgs, rsf.SSHFSAdditionalArgs...)
	rsf.sshCmd = exec.Command(sshBinary, sshArgs...)
	rsf.sshCmd.Stderr = os.Stderr
	driver := rsf.Driver
	opensshSftpServerBinary := rsf.OpensshSftpServerBinary
	switch driver {
	case DriverBuiltin, DriverOpensshSftpServer:
		// NOP
	case "", DriverAuto:
		var err error
		driver, opensshSftpServerBinary, err = DetectDriver(opensshSftpServerBinary)
		if err != nil {
			return fmt.Errorf("failed to choose driver automatically: %w", err)
		}
		logrus.Debugf("Chosen driver %q", driver)
	default:
		return fmt.Errorf("unknown driver %q", driver)
	}
	var builtinSftpServer *sftp.Server
	switch driver {
	case DriverBuiltin:
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
		builtinSftpServer, err = sftp.NewServer(stdio, sftpOpts...)
		if err != nil {
			return err
		}
	case DriverOpensshSftpServer:
		if opensshSftpServerBinary == "" {
			opensshSftpServerBinary = DetectOpensshSftpServerBinary()
			if opensshSftpServerBinary == "" {
				return errors.New("no openssh sftp-server found")
			}
		}
		logrus.Debugf("Using OpenSSH SFTP Server %q", opensshSftpServerBinary)
		sftpServerArgs := []string{
			// `-e` available since OpenSSH 5.4p1 (2010) https://github.com/openssh/openssh-portable/commit/7bee06ab
			"-e",
			// `-d` available since OpenSSH 6.2p1 (2013) https://github.com/openssh/openssh-portable/commit/502ab0ef
			// NOTE: `-d` just chdirs the sftp server process to the specified directory.
			// This is expected to be used in conjunction with chroot (in future), however, macOS does not support unprivileged chroot.
			"-d", strings.ReplaceAll(rsf.LocalPath, "%", "%%"),
		}
		if rsf.Readonly {
			// `-R` available since OpenSSH 5.4p1 (2010) https://github.com/openssh/openssh-portable/commit/db7bf825
			sftpServerArgs = append(sftpServerArgs, "-R")
		}
		rsf.opensshSftpServerCmd = exec.Command(opensshSftpServerBinary, sftpServerArgs...)
		rsf.opensshSftpServerCmd.Stderr = os.Stderr
		var err error
		rsf.opensshSftpServerCmd.Stdin, err = rsf.sshCmd.StdoutPipe()
		if err != nil {
			return err
		}
		rsf.sshCmd.Stdin, err = rsf.opensshSftpServerCmd.StdoutPipe()
		if err != nil {
			return err
		}
	}
	logrus.Debugf("executing ssh for remote sshfs: %s %v", rsf.sshCmd.Path, rsf.sshCmd.Args)
	if err := rsf.sshCmd.Start(); err != nil {
		return err
	}
	logrus.Debugf("starting sftp server for %v", rsf.LocalPath)
	switch driver {
	case DriverBuiltin:
		go func() {
			if srvErr := builtinSftpServer.Serve(); srvErr != nil {
				if errors.Is(srvErr, io.EOF) {
					logrus.WithError(srvErr).Debugf("sftp server for %v exited with EOF (negligible)", rsf.LocalPath)
				} else {
					logrus.WithError(srvErr).Errorf("sftp server for %v exited", rsf.LocalPath)
				}
			}
		}()
	case DriverOpensshSftpServer:
		logrus.Debugf("executing OpenSSH SFTP Server: %s %v", rsf.opensshSftpServerCmd.Path, rsf.opensshSftpServerCmd.Args)
		if err := rsf.opensshSftpServerCmd.Start(); err != nil {
			return err
		}
	}
	logrus.Debugf("waiting for remote ready")
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
  # spaces in file names are encoded as '\040' in the mount table
  if mount | sed 's/\\040/ /g' | grep "on ${dir}" | egrep -qw "fuse.sshfs|osxfuse"; then
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
	logrus.Debugf("killing processes for remote sshfs: %s %v", rsf.sshCmd.Path, rsf.sshCmd.Args)
	var errors []error
	if rsf.sshCmd != nil && rsf.sshCmd.Process != nil {
		if err := rsf.sshCmd.Process.Kill(); err != nil {
			errors = append(errors, err)
		}
	}
	if rsf.opensshSftpServerCmd != nil && rsf.opensshSftpServerCmd.Process != nil {
		if err := rsf.opensshSftpServerCmd.Process.Kill(); err != nil {
			errors = append(errors, err)
		}
	}
	if len(errors) > 0 {
		return fmt.Errorf("%v", errors)
	}
	return nil
}
