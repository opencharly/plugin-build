package build

import (
	"testing"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

// TestInitDependsCollector_ObservesTheRealInjector is the R10 wiring assertion: the collector is
// installed through the PUBLIC seam and the warning that arrives comes from
// deploykit.InjectInitDependsCandy itself, not from a call the test invented.
//
// It reproduces the exact condition opencharly/sdk#519 was found in — a box whose composition
// triggers the supervisord init while the supervisord CANDY is absent from the scanned set — and
// asserts the collector turns that report into a counted warning. Without the installation in
// resolveProjectEnvelopeTolerant this test still passes; without the COLLECTOR it fails, which is
// the half the previous unit tests already covered. What it adds is that the report is real: the
// injector, the dedupe and the message are the shipped ones.
func TestInitDependsCollector_ObservesTheRealInjector(t *testing.T) {
	cfg := &spec.Config{Box: spec.BoxMap{}}
	cfg.SetBox("demo", spec.BoxConfig{Base: "quay.io/fedora/fedora:43", Candy: []string{"ripgrep", "sshd"}})

	layers := map[string]deploykit.CandyModel{
		"ripgrep": deploykit.NewSpecCandyModel(spec.CandyModel{Name: "ripgrep"}, spec.CandyView{Name: "ripgrep"}),
		// sshd triggers the supervisord init via a service: field ...
		"sshd": deploykit.NewSpecCandyModel(
			spec.CandyModel{Name: "sshd"},
			spec.CandyView{Name: "sshd", InitSystems: map[string]bool{"supervisord": true}},
		),
		// ... and the supervisord CANDY is deliberately ABSENT: that is the condition.
	}
	initCfg := &spec.InitConfig{Init: map[string]*spec.ResolvedInit{
		"supervisord": {
			CandyFields:   []string{"service"},
			DependsCandy:  "supervisord",
			ServiceSchema: &spec.InitServiceSchema{ServiceTemplate: "[program:{{.Name}}]"},
		},
	}}

	var diags spec.Diagnostics
	prev := deploykit.SetUnsatisfiedInitDependsReporter(initDependsCollector(&diags))
	defer deploykit.SetUnsatisfiedInitDependsReporter(prev)

	deploykit.InjectInitDependsCandy(cfg, layers, initCfg)

	if len(diags.Items) != 1 {
		t.Fatalf("the injector reported %d time(s); want exactly 1 counted warning (diags=%v)", len(diags.Items), diags.Items)
	}
	if diags.Items[0].Severity != spec.SeverityWarning {
		t.Errorf("severity = %q, want %q", diags.Items[0].Severity, spec.SeverityWarning)
	}
	if got, want := diags.Items[0].Message, deploykit.UnsatisfiedInitDependsMessage("demo", "supervisord", "supervisord"); got != want {
		t.Errorf("counted text must equal the printed text:\n got: %q\nwant: %q", got, want)
	}
}
