package reversesshfs

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/AkihiroSuda/sshocker/pkg/ssh"
	"github.com/AkihiroSuda/sshocker/pkg/util"
	"github.com/pkg/errors"
	"github.com/pkg/sftp"
	"github.com/sirupsen/logrus"
)

type ReverseSSHFS struct {
	*ssh.SSHConfig
	LocalPath  string
	Host       string
	RemotePath string
	Readonly   bool
	sshCmd     *exec.Cmd
}

func (rsf *ReverseSSHFS) Prepare() error {
	sshBinary := rsf.SSHConfig.Binary()
	sshArgs := rsf.SSHConfig.Args()
	if !filepath.IsAbs(rsf.RemotePath) {
		return errors.Errorf("unexpected relative path: %q", rsf.RemotePath)
	}
	sshArgs = append(sshArgs, rsf.Host, "--", "mkdir", "-p", rsf.RemotePath)
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
	sshArgs = append(sshArgs, rsf.Host, "--", "sshfs", ":"+rsf.LocalPath, rsf.RemotePath, "-o", "slave")
	if rsf.Readonly {
		sshArgs = append(sshArgs, "-o", "ro")
	}
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
	return nil
}

func (rsf *ReverseSSHFS) Close() error {
	logrus.Debugf("killing ssh server for remote sshfs: %s %v", rsf.sshCmd.Path, rsf.sshCmd.Args)
	if err := rsf.sshCmd.Process.Kill(); err != nil {
		return err
	}
	return nil
}
