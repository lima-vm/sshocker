package sshocker

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"github.com/lima-vm/sshocker/pkg/mount"
	"github.com/lima-vm/sshocker/pkg/reversesshfs"
	"github.com/lima-vm/sshocker/pkg/ssh"
	"github.com/sirupsen/logrus"
)

type Sshocker struct {
	*ssh.SSHConfig
	Host                    string   // Required
	Port                    int      // Required
	Command                 []string // Optional
	Mounts                  []mount.Mount
	LForwards               []string
	SSHFSAdditionalArgs     []string
	Driver                  reversesshfs.Driver
	OpensshSftpServerBinary string
}

func (x *Sshocker) Run() error {
	if x.SSHConfig == nil {
		return errors.New("got nil SSHConfig")
	}
	sshBinary := x.SSHConfig.Binary()
	args := x.SSHConfig.Args()
	for _, l := range x.LForwards {
		args = append(args, "-L", l)
	}
	if x.Port != 0 {
		args = append(args, "-p", strconv.Itoa(x.Port))
	}
	args = append(args, x.Host, "--")
	if len(x.Command) > 0 {
		args = append(args, x.Command...)
	}
	cmd := exec.Command(sshBinary, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	for _, m := range x.Mounts {
		switch m.Type {
		case mount.MountTypeReverseSSHFS:
			rsf := &reversesshfs.ReverseSSHFS{
				Driver:                  x.Driver,
				OpensshSftpServerBinary: x.OpensshSftpServerBinary,
				SSHConfig:               x.SSHConfig,
				LocalPath:               m.Source,
				Host:                    x.Host,
				Port:                    x.Port,
				RemotePath:              m.Destination,
				Readonly:                m.Readonly,
				SSHFSAdditionalArgs:     x.SSHFSAdditionalArgs,
			}
			if err := rsf.Prepare(); err != nil {
				return fmt.Errorf("failed to prepare mounting %q (local) onto %q (remote): %w", rsf.LocalPath, rsf.RemotePath, err)
			}
			if err := rsf.Start(); err != nil {
				return fmt.Errorf("failed to mount %q (local) onto %q (remote): %w", rsf.LocalPath, rsf.RemotePath, err)
			}
			defer func() {
				if cErr := rsf.Close(); cErr != nil {
					logrus.WithError(cErr).Warnf("failed to unmount %q (remote)", rsf.RemotePath)
				}
			}()
		case mount.MountTypeInvalid:
			return fmt.Errorf("invalid mount type %v", m.Type)
		default:
			return fmt.Errorf("unknown mount type %v", m.Type)
		}
	}
	defer func() {
		if x.SSHConfig.Persist {
			if emErr := ssh.ExitMaster(x.Host, x.Port, x.SSHConfig); emErr != nil {
				logrus.WithError(emErr).Error("failed to exit the master")
			}
		}
	}()
	logrus.Debugf("executing main SSH: %s %v", cmd.Path, cmd.Args)
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}
