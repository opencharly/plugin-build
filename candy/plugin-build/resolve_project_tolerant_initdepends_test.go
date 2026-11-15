package build

import (
	"strings"
	"testing"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

// TestInitDependsCollector_CountsWhatItPrints is the R7 assertion for opencharly/sdk#519: the
// warning an operator READS and the warning the summary COUNTS must be the same event, carrying
// the same text. Before this, the line printed and `box validate` said "0 warnings, 0 errors".
func TestInitDependsCollector_CountsWhatItPrints(t *testing.T) {
	var diags spec.Diagnostics
	collect := initDependsCollector(&diags)
	collect("comfyui-lab", "supervisord", "supervisord")

	if len(diags.Items) != 1 {
		t.Fatalf("want exactly 1 diagnostic from one report, got %d", len(diags.Items))
	}
	got := diags.Items[0]
	if got.Severity != spec.SeverityWarning {
		t.Errorf("severity = %q, want %q — a warning that is recorded as an error (or not at all) "+
			"cannot fix the count it exists to fix", got.Severity, spec.SeverityWarning)
	}
	want := deploykit.UnsatisfiedInitDependsMessage("comfyui-lab", "supervisord", "supervisord")
	if got.Message != want {
		t.Errorf("the counted text must BE the printed text, so the two surfaces cannot drift:\n got: %q\nwant: %q",
			got.Message, want)
	}
	if !strings.Contains(got.Message, `resolves init "supervisord"`) {
		t.Errorf("message does not name the init: %q", got.Message)
	}
}

// TestInitDependsCollector_Accumulates — every report lands; the collector never swallows one.
// (The SDK dedupes per (box, init, candy) at the CALL path, so a repeat here is a genuine second
// finding, not a double-count.)
func TestInitDependsCollector_Accumulates(t *testing.T) {
	var diags spec.Diagnostics
	collect := initDependsCollector(&diags)
	collect("box-a", "supervisord", "supervisord")
	collect("box-b", "supervisord", "supervisord")
	if len(diags.Items) != 2 {
		t.Fatalf("want 2 diagnostics, got %d", len(diags.Items))
	}
}
