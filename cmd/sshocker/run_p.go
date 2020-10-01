package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

// parseFlagP parses -p flag, akin to `docker run -p` flags.
// The returned value conforms to the `ssh -L` syntax
func parseFlagP(s string) (string, error) {
	split := strings.Split(s, ":")
	switch len(split) {
	case 1:
		port, err := strconv.Atoi(split[0])
		if err != nil {
			return "", errors.Errorf("invalid port %q", split[0])
		}
		return fmt.Sprintf("0.0.0.0:%d:localhost:%d", port, port), nil
	case 2:
		localPort, err := strconv.Atoi(split[0])
		if err != nil {
			return "", errors.Errorf("invalid port %q", split[0])
		}
		remotePort, err := strconv.Atoi(split[1])
		if err != nil {
			return "", errors.Errorf("invalid port %q", split[1])
		}
		return fmt.Sprintf("0.0.0.0:%d:localhost:%d", localPort, remotePort), nil
	case 3:
		localIP := split[0]
		localPort, err := strconv.Atoi(split[1])
		if err != nil {
			return "", errors.Errorf("invalid port %q", split[1])
		}
		remotePort, err := strconv.Atoi(split[2])
		if err != nil {
			return "", errors.Errorf("invalid port %q", split[2])
		}
		return fmt.Sprintf("%s:%d:localhost:%d", localIP, localPort, remotePort), nil
	}
	return "", errors.Errorf("cannot parse %q, should be [[LOCALIP:]LOCALPORT:]REMOTEPORT", s)
}
