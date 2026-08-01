package buildinfo

import (
	"runtime/debug"
	"strconv"
	"testing"
)

func TestResolve(t *testing.T) {
	tests := []struct {
		name  string
		stamp string
		info  *debug.BuildInfo
		ok    bool
		want  string
	}{
		{name: "linker stamp wins", stamp: "v0.1.0-3-gabc123", info: vcsBuildInfo("deadbeef", true), ok: true, want: "v0.1.0-3-gabc123"},
		{name: "clean VCS revision", info: vcsBuildInfo("abc123", false), ok: true, want: "abc123"},
		{name: "dirty VCS revision", info: vcsBuildInfo("abc123", true), ok: true, want: "abc123+dirty"},
		{name: "missing revision", info: vcsBuildInfo("", true), ok: true, want: "development"},
		{name: "unavailable build info", info: vcsBuildInfo("abc123", true), want: "development"},
		{name: "missing build info", ok: true, want: "development"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resolve(test.stamp, test.info, test.ok); got != test.want {
				t.Fatalf("resolve(%q, %#v, %v) = %q, want %q", test.stamp, test.info, test.ok, got, test.want)
			}
		})
	}
}

func vcsBuildInfo(revision string, modified bool) *debug.BuildInfo {
	return &debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "vcs", Value: "git"},
		{Key: "vcs.revision", Value: revision},
		{Key: "vcs.time", Value: "2026-08-01T00:00:00Z"},
		{Key: "vcs.modified", Value: strconv.FormatBool(modified)},
	}}
}
