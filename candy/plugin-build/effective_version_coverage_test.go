package build

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/opencharly/sdk/buildkit"
	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

// TestResolveBuildEngine_DoesNotPopulateEffectiveVersion is the coverage that asserts AGAINST THE
// CHANGED FUNCTION. It parses `resolve.go` and walks the body of `resolveBuildEngine` — the function
// this PR edits — and FAILS if it (a) calls `ComputeEffectiveVersions` or (b) assigns any
// `.EffectiveVersion` field. `deploykit.ComputeEffectiveVersions` was the sole writer of
// `ResolvedBox.EffectiveVersion` on the resolve path and was deleted by sdk#313; re-introducing a
// population step here turns this test red. (The deleted symbol no longer exists in the merged sdk,
// so a re-introduction would also fail to compile — this test guards the plugin-owned call site
// against a future re-add once/if such a symbol returns, and against any other `.EffectiveVersion`
// assignment creeping into the orchestration.)
func TestResolveBuildEngine_DoesNotPopulateEffectiveVersion(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "resolve.go", nil, 0)
	if err != nil {
		t.Fatalf("parse resolve.go: %v", err)
	}
	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == "resolveBuildEngine" {
			fn = fd
			break
		}
	}
	if fn == nil {
		t.Fatal("resolveBuildEngine not found in resolve.go — this test would be vacuous")
	}

	var problems []string
	ast.Inspect(fn, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			switch fun := x.Fun.(type) {
			case *ast.SelectorExpr:
				if fun.Sel.Name == "ComputeEffectiveVersions" {
					problems = append(problems, fmt.Sprintf("call to ComputeEffectiveVersions at %s", fset.Position(x.Pos())))
				}
			case *ast.Ident:
				if fun.Name == "ComputeEffectiveVersions" {
					problems = append(problems, fmt.Sprintf("call to ComputeEffectiveVersions at %s", fset.Position(x.Pos())))
				}
			}
		case *ast.AssignStmt:
			for _, lhs := range x.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "EffectiveVersion" {
					problems = append(problems, fmt.Sprintf("assignment to .EffectiveVersion at %s", fset.Position(x.Pos())))
				}
			}
		}
		return true
	})
	if len(problems) > 0 {
		t.Fatalf("resolveBuildEngine must not populate EffectiveVersion (its sole writer, deploykit.ComputeEffectiveVersions, was deleted by the schema-versioning removal); found:\n- %s", strings.Join(problems, "\n- "))
	}
}

// TestResolvePipeline_LeavesEffectiveVersionUnset is the runtime half of the coverage: it drives the
// resolve chain `resolveBuildEngine` still calls after the removal
// (`buildkit.ResolveAllBox` → `deploykit.ComputeIntermediates` → `deploykit.GlobalCandyOrder`) over a
// fixture box and asserts `spec.ResolvedBox.EffectiveVersion` stays empty, so a future sdk-side
// population of the field on this chain is caught. It deliberately does NOT use
// `fullResolvedBoxFixture` / the byte-stable golden: those hardcode `EffectiveVersion` for the
// wire-projection completeness assertion and so say nothing about the resolve path.
func TestResolvePipeline_LeavesEffectiveVersionUnset(t *testing.T) {
	// A minimal, candy-free box so the resolve chain runs standalone: ComputeIntermediates /
	// GlobalCandyOrder need a layer for every referenced candy, and this test is about the
	// version field, not candy resolution.
	cfg := &spec.Config{
		Defaults: spec.BoxConfig{
			Base:      "quay.io/fedora/fedora:43",
			Platforms: []string{"linux/amd64"},
			Tag:       "auto",
			Registry:  "ghcr.io/test",
			Build:     []string{"rpm"},
		},
		Box: boxMapOfFixture(map[string]spec.BoxConfig{
			"demo": {Base: "quay.io/fedora/fedora:43"},
		}),
	}
	opts := resolveOptsFixture(spec.ResolveOpts{})

	resolved, err := buildkit.ResolveAllBox(cfg, "test", "", opts)
	if err != nil {
		t.Fatalf("ResolveAllBox: %v", err)
	}
	// Exactly the calls resolveBuildEngine still makes after the removal, in order.
	resolved, err = deploykit.ComputeIntermediates(resolved, map[string]deploykit.CandyModel{}, intermediateDefaults(cfg), "test")
	if err != nil {
		t.Fatalf("ComputeIntermediates: %v", err)
	}
	if _, err := deploykit.GlobalCandyOrder(resolved, map[string]deploykit.CandyModel{}); err != nil {
		t.Fatalf("GlobalCandyOrder: %v", err)
	}

	if len(resolved) == 0 {
		t.Fatal("resolve produced no boxes — fixture regression, test would be vacuous")
	}
	for name, box := range resolved {
		if box.EffectiveVersion != "" {
			t.Errorf("resolved[%q].EffectiveVersion = %q, want empty: the schema-versioning removal deleted the field's sole writer (deploykit.ComputeEffectiveVersions) and the ai.opencharly.version label is never emitted", name, box.EffectiveVersion)
		}
	}
}
