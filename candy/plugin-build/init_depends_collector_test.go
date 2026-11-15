package build

import (
	"testing"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

// TestInitDependsCollector_RecordsWithoutPrinting pins the ONE behaviour this collector must have:
// it turns the SDK's warning into a counted diagnostic and does NOT print it again.
//
// The printing is not an oversight to add back: the host's own project resolve already prints this
// line for the same command, in the host process, while the validate resolve runs in this one — so
// a printing collector showed the operator the identical warning twice (measured on the fixture:
// two prints, one count). This test fails if the print returns.
func TestInitDependsCollector_RecordsWithoutPrinting(t *testing.T) {
	var diags spec.Diagnostics
	initDependsCollector(&diags)("demo-box", "supervisord", "supervisord")

	if len(diags.Items) != 1 {
		t.Fatalf("want exactly 1 diagnostic, got %d", len(diags.Items))
	}
	got := diags.Items[0]
	if got.Severity != spec.SeverityWarning {
		t.Errorf("severity = %q, want %q — a warning counted as an error would fail a valid project", got.Severity, spec.SeverityWarning)
	}
	want := deploykit.UnsatisfiedInitDependsMessage("demo-box", "supervisord", "supervisord")
	if got.Message != want {
		t.Errorf("counted text must BE the SDK's one message:\n got: %q\nwant: %q", got.Message, want)
	}
}

// TestInstallInitDependsReporter_IsTheProductionInstaller drives the installer the validate path
// defers, through the REAL injection, so a change that stops the collector from firing breaks this.
func TestInstallInitDependsReporter_IsTheProductionInstaller(t *testing.T) {
	cfg := &spec.Config{Box: spec.BoxMap{}}
	cfg.SetBox("demo", spec.BoxConfig{Base: "quay.io/fedora/fedora-minimal:43", Candy: []string{"svc"}})

	layers := map[string]deploykit.CandyModel{
		"svc": deploykit.NewSpecCandyModel(
			spec.CandyModel{Name: "svc"},
			spec.CandyView{Name: "svc", InitSystems: map[string]bool{"supervisord": true}},
		),
		// the supervisord CANDY is deliberately absent: that is the condition
	}
	initCfg := &spec.InitConfig{Init: map[string]*spec.ResolvedInit{
		"supervisord": {CandyFields: []string{"service"}, DependsCandy: "supervisord",
			ServiceSchema: &spec.InitServiceSchema{ServiceTemplate: "[program:{{.Name}}]"}},
	}}

	var diags spec.Diagnostics
	restore := installInitDependsReporter(&diags)
	defer restore()

	deploykit.InjectInitDependsCandy(cfg, layers, initCfg)

	if len(diags.Items) != 1 || diags.Items[0].Severity != spec.SeverityWarning {
		t.Fatalf("the production installer must record exactly one warning from the real injector; got %v", diags.Items)
	}
}
