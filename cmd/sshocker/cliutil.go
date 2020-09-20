package main

import (
	"fmt"

	"github.com/urfave/cli/v2"
)

// setHidden derived from https://github.com/aquasecurity/trivy/blob/v0.11.0/internal/app.go#L273-L298 (Apache License 2.0)
func setHidden(flags []cli.Flag, hidden bool) []cli.Flag {
	var newFlags []cli.Flag
	for _, flag := range flags {
		var f cli.Flag
		switch pf := flag.(type) {
		case *cli.StringFlag:
			stringFlag := *pf
			stringFlag.Hidden = hidden
			f = &stringFlag
		case *cli.StringSliceFlag:
			stringSliceFlag := *pf
			stringSliceFlag.Hidden = hidden
			f = &stringSliceFlag
		case *cli.BoolFlag:
			boolFlag := *pf
			boolFlag.Hidden = hidden
			f = &boolFlag
		case *cli.IntFlag:
			intFlag := *pf
			intFlag.Hidden = hidden
			f = &intFlag
		case *cli.DurationFlag:
			durationFlag := *pf
			durationFlag.Hidden = hidden
			f = &durationFlag
		default:
			panic(fmt.Errorf("unknown flag type: %+v", pf))
		}
		newFlags = append(newFlags, f)
	}
	return newFlags
}
