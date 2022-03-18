package main

import (
	"errors"
	"fmt"
	"net"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lima-vm/sshocker/pkg/mount"
	"github.com/lima-vm/sshocker/pkg/ssh"
	"github.com/lima-vm/sshocker/pkg/sshocker"
	"github.com/urfave/cli/v2"
)

var (
	runFlags = []cli.Flag{
		&cli.StringFlag{
			Name:    "ssh-config",
			Aliases: []string{"F"},
			Usage:   "ssh config file",
		},
		&cli.BoolFlag{
			Name:  "ssh-persist",
			Usage: "enable ControlPersist",
			Value: true,
		},
		&cli.StringSliceFlag{
			Name: "v",
			Usage: "Mount a reverse SSHFS, " +
				"e.g. `.:/mnt/ssh` to mount the current directory on the client onto /mnt/ssh on the server, " +
				"append `:ro` for read-only mount",
		},
		&cli.StringSliceFlag{
			Name:  "p",
			Usage: "Expose a port, e.g. `8080:80` to forward the port 8080 the client onto the port 80 on the server",
		},
		&cli.BoolFlag{
			Name:  "sshfs-nonempty",
			Usage: "enable sshfs nonempty",
			Value: false,
		},
	}
	runCommand = &cli.Command{
		Name:   "run",
		Usage:  "Akin to `docker run` (The default subcommand)",
		Action: runAction,
		Flags:  runFlags,
	}
)

func parseHost(s string) (string, int, error) {
	if !strings.Contains(s, ":") {
		// FIXME: this check is not valid for IPv6!
		return s, 0, nil
	}
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, err
	}
	// host may contain "user@" prefix.
	return host, port, nil
}

func runAction(clicontext *cli.Context) error {
	if clicontext.NArg() < 1 {
		return errors.New("no host specified")
	}
	sshConfig := &ssh.SSHConfig{
		ConfigFile: clicontext.String("ssh-config"),
		Persist:    clicontext.Bool("ssh-persist"),
	}
	host, port, err := parseHost(clicontext.Args().First())
	if err != nil {
		return err
	}
	var sshfsAdditionalArgs []string
	if clicontext.Bool("sshfs-nonempty") {
		sshfsAdditionalArgs = append(sshfsAdditionalArgs, "-o", "nonempty")
	}
	x := &sshocker.Sshocker{
		SSHConfig:           sshConfig,
		Host:                host,
		Port:                port,
		Command:             clicontext.Args().Tail(),
		SSHFSAdditionalArgs: sshfsAdditionalArgs,
	}
	if len(x.Command) > 0 && x.Command[0] == "--" {
		x.Command = x.Command[1:]
	}
	for _, v := range clicontext.StringSlice("v") {
		m, err := parseFlagV(v)
		if err != nil {
			return err
		}
		x.Mounts = append(x.Mounts, m)
	}
	for _, p := range clicontext.StringSlice("p") {
		lforward, err := parseFlagP(p)
		if err != nil {
			return err
		}
		x.LForwards = append(x.LForwards, lforward)
	}
	return x.Run()
}

func expandLocalPath(localPath string) (string, error) {
	s := localPath
	if s == "" {
		return "", errors.New("got empty local path")
	}
	if strings.HasPrefix(s, "~/") {
		u, err := user.Current()
		if err != nil {
			return "", err
		}
		if u.HomeDir == "" {
			return "", errors.New("cannot determine the local home directory")
		}
		s = strings.Replace(s, "~", u.HomeDir, 1)
	}
	return filepath.Abs(s)
}

// parseFlagV parses -v flag, akin to `docker run -v` flags.
func parseFlagV(s string) (mount.Mount, error) {
	m := mount.Mount{
		Type: mount.MountTypeReverseSSHFS,
	}
	// TODO: support Windows. How does `docker run -v` work with Windows drive letters?
	split := strings.Split(s, ":")
	switch len(split) {
	case 2:
		m.Source = split[0]
		m.Destination = split[1]
	case 3:
		m.Source = split[0]
		m.Destination = split[1]
		if split[2] == "ro" {
			m.Readonly = true
		} else {
			return m, fmt.Errorf("cannot parse %q: unknown option %q", s, split[2])
		}
	default:
		return m, fmt.Errorf("cannot parse %q", s)
	}
	var err error
	m.Source, err = expandLocalPath(m.Source)
	if err != nil {
		return m, fmt.Errorf("cannot use %q: %w", s, err)
	}
	return m, nil
}
