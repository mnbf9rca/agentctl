// Package buildinfo reports the identity recorded in the current binary.
package buildinfo

import "runtime/debug"

// Stamp is set by project builds with a linker -X flag.
var Stamp string

// Current returns the most precise build identity recorded in the binary.
func Current() string {
	info, ok := debug.ReadBuildInfo()
	return resolve(Stamp, info, ok)
}

func resolve(stamp string, info *debug.BuildInfo, ok bool) string {
	if stamp != "" {
		return stamp
	}
	if !ok || info == nil {
		return "development"
	}

	var revision string
	modified := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision == "" {
		return "development"
	}
	if modified {
		return revision + "+dirty"
	}
	return revision
}
