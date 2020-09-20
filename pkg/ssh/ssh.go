package ssh

import (
	"os"
	"os/exec"
	"strconv"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type SSHConfig struct {
	Persist bool
	// TODO: ssh-config, priv key, pub key, ...
}

func (c *SSHConfig) Binary() string {
	return "ssh"
}

func (c *SSHConfig) Args() []string {
	var args []string
	if c.Persist {
		args = append(args,
			"-o", "ControlMaster=auto",
			// TODO: Does this work on Windows?
			"-o", "ControlPath=~/.ssh/sshocker-%r@%h:%p-"+strconv.Itoa(os.Getpid()),
			"-o", "ControlPersist=yes",
		)
	}
	return args
}

// ExitMaster executes `ssh -O exit`
func ExitMaster(host string, c *SSHConfig) error {
	if c == nil {
		return errors.New("got nil SSHConfig")
	}
	args := c.Args()
	args = append(args, "-O", "exit", host)
	cmd := exec.Command(c.Binary(), args...)
	logrus.Debugf("executing ssh for exiting the master: %s %v", cmd.Path, cmd.Args)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to execute `%s -O exit %s`, out=%q", c.Binary(), host, string(out))
	}
	return nil
}
