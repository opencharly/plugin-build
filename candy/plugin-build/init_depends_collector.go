package build

import (
	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

// init_depends_collector.go — the ONE place the init-`depends_candy` warning is turned from a
// stderr line into a counted diagnostic.
//
// WHERE THE EVENT HAPPENS (measured, opencharly/sdk#519): the namespaced-box fill
// (deploykit.FillNamespaceBoxViews) runs inside this package's resolve flow — resolve.go's
// projectResolvedProjectLeg -> the LoadSeams.FillNamespacedBoxes seam -> fillNamespacedBoxes. So
// the reporter must be installed HERE, around that projection, and its findings must ride the
// build-resolve reply. Two earlier placements were measured to do nothing: plugin-box never emits
// it, and a reporter installed on the OpValidate path never fires because the fill happens under
// THIS op. The reply only gained a diagnostics field in opencharly/spec#330.

// initDependsCollector makes the warning COUNTABLE without printing it a second time.
//
// The host's own project resolve already prints this line for the same command, in the host
// process, and the validate resolve runs in the plugin process — so a collector that also printed
// would show the operator the identical warning twice (measured on the fixture: two prints, one
// count). The collector therefore RECORDS the SDK's ONE message text and leaves the printing to the
// resolve that already does it.
func initDependsCollector(diags *spec.Diagnostics) func(boxName, initName, dependsCandy string) {
	return func(boxName, initName, dependsCandy string) {
		diags.Items = append(diags.Items, spec.Diagnostic{
			Severity: spec.SeverityWarning,
			Message:  deploykit.UnsatisfiedInitDependsMessage(boxName, initName, dependsCandy),
		})
	}
}

// installInitDependsReporter installs initDependsCollector and returns the function that restores
// the previous reporter. Named so a test drives the production installer rather than a copy of it.
func installInitDependsReporter(diags *spec.Diagnostics) func() {
	prev := deploykit.SetUnsatisfiedInitDependsReporter(initDependsCollector(diags))
	return func() { deploykit.SetUnsatisfiedInitDependsReporter(prev) }
}
