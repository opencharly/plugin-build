package build

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/opencharly/spec/spec"
)

// diagWarnSink is what makes scan advisories COUNTABLE. Before it they were stderr writes in
// sdk/loaderkit, so `charly box validate` could not report how many warnings a run produced —
// an early draft of its summary printed "0 warnings" on a run that had just emitted two.
func TestDiagWarnSinkCollectsAdvisoriesAsWarnings(t *testing.T) {
	var diags spec.Diagnostics
	warn := diagWarnSink(&diags)

	warn("candy %s resolved to multiple versions; using newest %s", "acme/thing", "2026.242.1655")
	warn("local candy %q shadows remote candy %q", "punktfunk", "github.com/o/punktfunk")

	if len(diags.Items) != 2 {
		t.Fatalf("expected 2 diagnostics, got %d", len(diags.Items))
	}
	for i, it := range diags.Items {
		if it.Severity != "warning" {
			t.Errorf("item %d severity = %q, want \"warning\" — an advisory must never be an error", i, it.Severity)
		}
	}
	if !strings.Contains(diags.Items[0].Message, "acme/thing") ||
		!strings.Contains(diags.Items[0].Message, "2026.242.1655") {
		t.Errorf("arguments must be formatted into the message, got %q", diags.Items[0].Message)
	}
}

// The sink must APPEND, not replace: a run emits several advisories and the count has to be
// the total, not the last one.
func TestDiagWarnSinkAppendsToExistingDiagnostics(t *testing.T) {
	diags := spec.Diagnostics{Items: []spec.Diagnostic{{Severity: "error", Message: "pre-existing"}}}
	diagWarnSink(&diags)("an advisory")
	if len(diags.Items) != 2 {
		t.Fatalf("expected the sink to append, got %d items", len(diags.Items))
	}
	if diags.Items[0].Message != "pre-existing" {
		t.Errorf("existing diagnostics must survive, got %q", diags.Items[0].Message)
	}
}

// The BUILD paths keep printing, because they have no diagnostics envelope to collect into.
// This pins that stderrWarn really writes to stderr and formats its arguments, so the two
// sinks cannot silently converge on the same behaviour.
func TestStderrWarnWritesToStderr(t *testing.T) {
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	stderrWarn("candy %s resolved to multiple versions", "acme/thing")
	w.Close()
	os.Stderr = orig

	var buf bytes.Buffer
	if _, cerr := io.Copy(&buf, r); cerr != nil {
		t.Fatalf("read: %v", cerr)
	}
	got := strings.TrimSpace(buf.String())
	if got != "candy acme/thing resolved to multiple versions" {
		t.Errorf("stderrWarn output = %q", got)
	}
}
