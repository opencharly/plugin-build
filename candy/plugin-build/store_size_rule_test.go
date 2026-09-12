package build

import (
	"os"
	"strings"
	"testing"
)

// TestNoPodmanStoreSizeRule is the R5 grep self-test in executable form (R7): the podman
// store-size rule — the `podman system df` probe in store_bloat.go and the host-state line it
// emitted — must not come back. The rule was removed because it reported a property of the
// LOCAL BUILD HOST inside a bed's output, where the org's R10 gate counts un-allowlisted
// lines, so a bloated store on the machine could gate an unrelated PR.
//
// This test FAILS on the tree that predates the removal (store_bloat.go is present and every
// needle below hits) and passes only while the rule is gone: coverage that cannot pass without
// the change, not a restatement of it.
func TestNoPodmanStoreSizeRule(t *testing.T) {
	forbidden := []string{
		"podman system df",
		"store is bloated",
		"storeBloat",
		"noticeIfStoreBloated",
		"warnIfStoreBloated",
		"parseStoreReclaimable",
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package dir: %v", err)
	}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		// This file necessarily spells the needles it forbids, and non-Go files carry no code.
		if e.IsDir() || !strings.HasSuffix(name, ".go") || name == "store_size_rule_test.go" {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		scanned++
		for _, needle := range forbidden {
			if strings.Contains(string(data), needle) {
				t.Errorf("%s still carries %q: the podman store-size rule was removed (R5) — "+
					"a host-state report about the build machine must not be emitted into a bed's "+
					"diagnostics, where it gates PRs unrelated to the change under test", name, needle)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no Go source — the sweep would pass vacuously")
	}
}
