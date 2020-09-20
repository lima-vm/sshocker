package ssh

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"strings"

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

// parseScriptInterpreter extracts "#!/bin/sh" interpreter string from the script.
// The result does not contain the "#!" prefix.
func parseScriptInterpreter(script string) (string, error) {
	r := bufio.NewReader(strings.NewReader(script))
	firstLine, partial, err := r.ReadLine()
	if err != nil {
		return "", errors.Wrapf(err, "cannot determine interpreter from script %q", script)
	}
	if partial {
		return "", errors.Errorf("cannot determine interpreter from script %q: cannot read the first line", script)
	}
	if !strings.HasPrefix(string(firstLine), "#!") {
		return "", errors.Errorf("cannot determine interpreter from script %q: the first line lacks `#!`", script)
	}
	interp := strings.TrimPrefix(string(firstLine), "#!")
	if interp == "" {
		return "", errors.Errorf("cannot determine interpreter from script %q: empty?", script)
	}
	return interp, nil
}

// ExecuteScript executes the given script on the remote host via stdin.
// Returns stdout and stderr.
//
// scriptName is used only for readability of error strings.
func ExecuteScript(host string, c *SSHConfig, script, scriptName string) (string, string, error) {
	if c == nil {
		return "", "", errors.New("got nil SSHConfig")
	}
	interpreter, err := parseScriptInterpreter(script)
	if err != nil {
		return "", "", err
	}
	sshBinary := c.Binary()
	sshArgs := c.Args()
	sshArgs = append(sshArgs, host, "--", interpreter)
	sshCmd := exec.Command(sshBinary, sshArgs...)
	sshCmd.Stdin = strings.NewReader(script)
	var stderr bytes.Buffer
	sshCmd.Stderr = &stderr
	logrus.Debugf("executing ssh for script %q: %s %v", scriptName, sshCmd.Path, sshCmd.Args)
	out, err := sshCmd.Output()
	if err != nil {
		return string(out), stderr.String(), errors.Wrapf(err, "failed to execute script %q: stdout=%q, stderr=%q",
			scriptName, string(out), stderr.String())
	}
	return string(out), stderr.String(), nil
}
