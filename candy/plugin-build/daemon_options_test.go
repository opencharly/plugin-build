package build

import (
	"strings"
	"testing"

	"github.com/opencharly/sdk/buildkitdrive"
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

// TestDaemonOptions_RendersTheDaemonConfig pins the mapping's END-TO-END artifact: the TOML
// the drive actually writes must carry the authored values in buildkitd's accepted spellings.
//
// The casing is MIXED BY DESIGN and daemon-verified: `process-mode`/`max-parallelism` are
// buildkitd's TOML keys for those two worker settings, the GC/worker fields are camelCase. A
// real buildkitd started from this exact render reports `oci.process-mode:no-sandbox` and
// `worker.snapshotter:overlayfs` on its worker, so asserting the rendered text is asserting
// what the daemon loads — not an internal struct.
func TestDaemonOptions_RendersTheDaemonConfig(t *testing.T) {
	gc := true
	c := driveConfig{
		StageParallelism: 4,
		Buildkit: &spec.Buildkit{
			ProcessMode:      "sandbox",
			MaxParallelism:   8,
			Snapshotter:      "overlayfs",
			NoProcessSandbox: true,
			GC:               &gc,
			ReservedSpace:    "40GB",
			MaxUsedSpace:     "80GB",
			MinFreeSpace:     "5GB",
			GCPolicy: []spec.BuildkitGCPolicy{{
				All:          true,
				MaxUsedSpace: "60GB",
				Filters:      []string{"type==source.local"},
			}},
		},
	}
	cfg := buildkitdrive.DaemonConfig(c.daemonOptions())
	for _, want := range []string{
		"[worker.oci]",
		`process-mode = "sandbox"`,
		"max-parallelism = 8",
		`snapshotter = "overlayfs"`,
		"noProcessSandbox = true",
		"[[worker.oci.gcpolicy]]",
		"all = true",
		`maxUsedSpace = "60GB"`,
		`filters = ["type==source.local"]`,
		`reservedSpace = "40GB"`,
		`maxUsedSpace = "80GB"`,
		`minFreeSpace = "5GB"`,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("the rendered daemon config must carry %q:\n%s", want, cfg)
		}
	}

	// Unauthored: the stage parallelism and sdk's 100 GB GC default still render, so a project
	// that authors nothing gets the cache-preserving daemon (opencharly/charly#1292).
	plain := buildkitdrive.DaemonConfig((driveConfig{StageParallelism: 4}).daemonOptions())
	for _, want := range []string{
		"max-parallelism = 4",
		`reservedSpace = "100GB"`,
		`maxUsedSpace = "100GB"`,
		`minFreeSpace = "10GB"`,
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("an unauthored project must still render %q:\n%s", want, plain)
		}
	}
}
