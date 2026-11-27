package build

import (
	"testing"

	"github.com/opencharly/spec/spec"
)

// TestDaemonOptions_MapsAuthoredBuildkit pins the defaults.buildkit surface onto the
// buildkitdrive daemon settings: every authored field wins, the GC disable is a pointer
// (absent = enabled with the package's 100 GB budget), and the registry mirror prefers the
// project over the env fallback.
func TestDaemonOptions_MapsAuthoredBuildkit(t *testing.T) {
	gcOff := false
	c := driveConfig{
		StageParallelism: 4,
		RegistryMirror:   "env-mirror:5000",
		Buildkit: &spec.Buildkit{
			ProcessMode:      "sandbox",
			MaxParallelism:   8,
			Snapshotter:      "overlayfs",
			NoProcessSandbox: true,
			GC:               &gcOff,
			ReservedSpace:    "40GB",
			MaxUsedSpace:     "80GB",
			MinFreeSpace:     "5GB",
			RegistryMirror:   "cfg-mirror:5000",
			Extra:            "# operator extra\n[log]\n  level = \"debug\"",
			GCPolicy: []spec.BuildkitGCPolicy{{
				All:          true,
				MaxUsedSpace: "1GB",
				Filters:      []string{"type==source.local"},
			}},
		},
	}
	o := c.daemonOptions()
	if o.MaxParallelism != 8 {
		t.Errorf("authored max_parallelism must win over stage_parallelism: got %d", o.MaxParallelism)
	}
	if o.RegistryMirror != "cfg-mirror:5000" {
		t.Errorf("authored registry_mirror must win over the env fallback: got %q", o.RegistryMirror)
	}
	if !o.GCDisabled || o.GCReservedSpace != "40GB" || o.GCMaxUsedSpace != "80GB" || o.GCMinFreeSpace != "5GB" {
		t.Errorf("authored GC fields must map: %+v", o)
	}
	if o.Snapshotter != "overlayfs" || !o.NoProcessSandbox || o.ProcessMode != "sandbox" {
		t.Errorf("authored worker fields must map: %+v", o)
	}
	if len(o.GCPolicies) != 1 || !o.GCPolicies[0].All || o.GCPolicies[0].MaxUsedSpace != "1GB" {
		t.Errorf("authored gcpolicy must map: %+v", o.GCPolicies)
	}
	if o.Extra == "" {
		t.Errorf("authored extra must map")
	}
}

// TestDaemonOptions_DefaultsWithoutAuthoredBuildkit: a project that authors no buildkit
// block still passes the drive's fallbacks; the GC budget itself defaults in
// sdk/buildkitdrive, so the cache-preserving policy applies without any project change.
func TestDaemonOptions_DefaultsWithoutAuthoredBuildkit(t *testing.T) {
	c := driveConfig{StageParallelism: 4, RegistryMirror: "env-mirror:5000"}
	o := c.daemonOptions()
	if o.MaxParallelism != 4 || o.RegistryMirror != "env-mirror:5000" {
		t.Errorf("unauthored settings must take the drive fallbacks: %+v", o)
	}
	if o.GCDisabled || o.GCReservedSpace != "" || o.GCMaxUsedSpace != "" || o.GCMinFreeSpace != "" {
		t.Errorf("the GC budget must be left to the sdk default when unauthored: %+v", o)
	}
}
