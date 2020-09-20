package main

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/AkihiroSuda/sshocker/pkg/mount"
	"github.com/AkihiroSuda/sshocker/pkg/ssh"
	"github.com/AkihiroSuda/sshocker/pkg/sshocker"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
)

func main() {
	debug := false
	app := cli.NewApp()
	app.Name = "sshocker"
	app.HideHelpCommand = true
	app.Usage = "ssh + reverse sshfs + port forwarder, in Docker-like CLI"
	app.UsageText = "sshocker USER@HOST"
	app.Flags = []cli.Flag{
		&cli.BoolFlag{
			Name:        "debug",
			Usage:       "debug mode",
			Destination: &debug,
		},
		&cli.BoolFlag{
			Name:  "ssh-persist",
			Usage: "enable ControlPersist",
			Value: true,
		},
		&cli.StringSliceFlag{
			Name: "v",
			Usage: "e.g. `.:/mnt/ssh` to mount the current directory on the client onto /mnt/ssh on the server," +
				"append `:ro` for read-only mount",
		},
		&cli.StringSliceFlag{
			Name:  "p",
			Usage: "e.g. `8080:80` to forward the port 8080 the client onto the port 80 on the server",
		},
	}
	app.Before = func(context *cli.Context) error {
		if debug {
			logrus.SetLevel(logrus.DebugLevel)
		}
		return nil
	}
	app.Action = func(clicontext *cli.Context) error {
		if clicontext.NArg() < 1 {
			return errors.New("no host specified")
		}
		sshConfig := &ssh.SSHConfig{
			Persist: clicontext.Bool("ssh-persist"),
		}
		x := &sshocker.Sshocker{
			SSHConfig: sshConfig,
			Host:      clicontext.Args().First(),
			Command:   clicontext.Args().Tail(),
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
	if err := app.Run(os.Args); err != nil {
		logrus.Fatal(err)
	}
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
			return m, errors.Errorf("cannot parse %q: unknown option %q", s, split[2])
		}
	default:
		return m, errors.Errorf("cannot parse %q", s)
	}
	var err error
	m.Source, err = expandLocalPath(m.Source)
	if err != nil {
		return m, errors.Wrapf(err, "cannot use %q", s)
	}
	return m, nil
}

// parseFlagP parses -p flag, akin to `docker run -p` flags.
// The returned value conforms to the `ssh -L` syntax
func parseFlagP(s string) (string, error) {
	split := strings.Split(s, ":")
	if len(split) >= 3 {
		return s, nil
	}
	if len(split) == 2 {
		return split[0] + ":localhost:" + split[1], nil
	}
	return "", errors.Errorf("cannot parse %q", s)
}
