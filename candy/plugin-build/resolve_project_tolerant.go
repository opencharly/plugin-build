package build

import (
	"context"
	"fmt"
	"os"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/buildkit"
	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/sdk/loaderkit"
	"github.com/opencharly/spec/spec"
)

// resolve_project_tolerant.go — the error-TOLERANT sibling of resolve_project_word.go's
// resolveProjectEnvelope (#55 step3 unit 3-I): relocates the TOLERANT half of
// charly/validate_project_host.go's former hostBuildValidateProject onto build:project (a NEW
// sdk.OpValidate leg on the SAME "project" word, alongside 3b's sdk.OpResolve — the class:word
// bijection gate (F11) is per (class,word), not per (class,word,op), so a second Op on an
// existing word is the SAME pattern candy/plugin-build already uses for build:box + build:generate).
//
// A LoadUnified/scan failure becomes a spec.Diagnostic (skip+continue) instead of aborting —
// mirroring the DELETED host buildResolvedProjectTolerant/loadProjectForResolve's tolerant
// branch — reusing the SAME loaderkit primitives resolveProjectEnvelope already proved portable
// in 3b. What does NOT relocate here are the RULES themselves — they belong to the validate plugin,
// not to this resolve: candy/plugin-box runs the raw-config, CUE-schema-conformance and
// remote-candy checks over its OWN self-load (validate_config_rules.go / validate_schema_rules.go,
// K-wave 2 cone R1 unit B), which is why the host's former "validate-project-checks" leg is DELETED.
// The one thing neither plugin can produce is the registry-derived D-data
// (ProviderCapabilities/ActCapableVerbs — needs providerRegistry.allProviders(), a genuine kernel
// M-mechanism per the boundary law); runValidateEngine gets that from the host's small
// "validate-word-sets" leg and folds it onto the envelope this Op returns.

// The tolerant-load failure severity is spec's closed-vocabulary SeverityError (spec#186) — the
// former local diagSeverityError raw "error" literal is gone with the one-domain cutover.

// resolveProjectEnvelopeTolerant is the TOLERANT project resolve: a LoadUnified or candy-scan
// failure is appended to diags and the resolve continues best-effort (empty envelope / empty
// layers) instead of aborting, so `charly box validate` can report findings on a project that
// fails to fully resolve. Mirrors resolveProjectEnvelope's steps exactly (load → vocab → scan →
// project), diverging ONLY in error handling — same RDD "byte-for-byte relocation" discipline 3b
// used, extended to the tolerant contract loadProjectForResolve/buildResolvedProjectTolerant had.
func resolveProjectEnvelopeTolerant(ctx context.Context, ex *sdk.Executor, req spec.ValidateProjectRequest) (spec.ResolvedProject, spec.Diagnostics) {
	var diags spec.Diagnostics
	addDiag := func(err error) {
		diags.Items = append(diags.Items, spec.Diagnostic{Severity: spec.SeverityError, Message: err.Error()})
	}

	// The init-dependency warning is PRINTED by this process (deploykit.InjectInitDependsCandy is
	// called from resolve.go HERE, not in plugin-box), so the count has to be taken HERE: a reporter
	// installed on the consumer side would never fire. Install one that keeps the identical stderr
	// line AND records a warning diagnostic, so the caller's summary counts what the operator reads
	// (opencharly/sdk#519 — measured: the warning printed and `box validate` said "0 warnings").
	// The installation is a NAMED function so the production wiring itself is testable: a test calls
	// installInitDependsReporter and drives the real injector under it, so deleting or changing this
	// line breaks a test instead of leaving every test green (opencharly/plugin-build#55, review
	// round 2: "every test stays green if it is deleted" was true of the first cut).
	defer installInitDependsReporter(&diags)()

	dir := req.Dir
	if dir == "" {
		if cwd, err := os.Getwd(); err == nil {
			dir = cwd
		}
	}

	uf, ok, err := loaderkit.LoadUnifiedViaExecutor(ctx, ex, dir)
	if err != nil {
		// A real load failure (schema gate, parse error, …) — tolerant: diagnose, empty envelope.
		addDiag(err)
		return spec.ResolvedProject{}, diags
	}
	if !ok || uf == nil {
		// A project-less directory is NOT an error (the same empty-project contract every
		// resolved-project consumer honours) — no diagnostic, just an empty envelope.
		return spec.ResolvedProject{}, diags
	}
	cfg := uf.ProjectConfig()

	distroCfg := loaderkit.ProjectDistroConfig(uf, resolveDistroLeg(ctx, ex))
	builderCfg := loaderkit.ProjectBuilderConfig(uf)
	initCfg := loaderkit.ProjectInitConfig(uf, resolveInitLeg(ctx, ex))

	resReq := spec.ResolvedProjectRequest{Dir: dir, IncludeDisabled: req.IncludeDisabled}
	layers := map[string]spec.CandyReader{}
	localScanned, err := scanLocalLeg(ctx, ex, uf, dir, distroCfg)
	if err != nil {
		addDiag(err)
	} else {
		scanned, serr := loaderkit.ScanCandyFromLocal(localScanned, initCfg, scanSeamsLeg(ctx, ex, resReq, cfg, distroCfg, diagWarnSink(&diags)))
		if serr != nil {
			addDiag(serr)
		} else {
			layers = scanned
		}
	}

	calver := buildkit.ComputeCalVer()
	// preResolvedBoxes=nil (a fresh per-box resolve, mirroring resolveProjectEnvelope); diags
	// non-nil makes loaderkit.ProjectResolvedProject SKIP a box whose ResolveBox fails instead of
	// aborting the whole projection (loaderkit.ProjectResolvedProject's own tolerant contract).
	rp, rerr := projectResolvedProjectLeg(ctx, ex, cfg, layers, uf, distroCfg, builderCfg, initCfg, dir, uf.Version, calver, req.IncludeDisabled, nil, &diags)
	if rerr != nil {
		// projectResolvedProjectLeg itself only returns a hard error for a non-box-resolve failure
		// (diags==nil never applies here since we always pass a non-nil diags) — tolerant: diagnose,
		// keep whatever partial rp came back (never nil per loaderkit.ProjectResolvedProject's
		// contract, but guarded below regardless).
		addDiag(rerr)
	}
	if rp == nil {
		rp = &spec.ResolvedProject{}
	}
	rp.Primaries = loaderkit.LoaderThreadedViaExecutor(ctx, ex).Primaries
	return *rp, diags
}

// initDependsCollector is the reporter that keeps the init-dependency warning VISIBLE and makes it
// COUNTABLE at once: it prints the SDK's ONE message to stderr (unchanged operator output) and
// appends the same text as a warning-severity diagnostic, which the caller's summary counts.
//
// A consumer-side reporter cannot do this job: during `charly box validate` the resolution — and
// therefore deploykit.InjectInitDependsCandy — runs in THIS process (resolve.go), so plugin-box
// would install a reporter that never fires (opencharly/sdk#519, process topology measured).
func initDependsCollector(diags *spec.Diagnostics) func(boxName, initName, dependsCandy string) {
	return func(boxName, initName, dependsCandy string) {
		msg := deploykit.UnsatisfiedInitDependsMessage(boxName, initName, dependsCandy)
		fmt.Fprintf(os.Stderr, "warning: %s\n", msg)
		diags.Items = append(diags.Items, spec.Diagnostic{Severity: spec.SeverityWarning, Message: msg})
	}
}

// installInitDependsReporter installs the collecting reporter and returns the function that restores
// the previous one. It exists as a NAMED seam so the production installation is covered by a test:
// the test calls THIS function and drives deploykit.InjectInitDependsCandy under it, which means
// removing the installation from resolveProjectEnvelopeTolerant breaks a test rather than silently
// leaving the count at zero.
func installInitDependsReporter(diags *spec.Diagnostics) func() {
	prev := deploykit.SetUnsatisfiedInitDependsReporter(initDependsCollector(diags))
	return func() { deploykit.SetUnsatisfiedInitDependsReporter(prev) }
}
