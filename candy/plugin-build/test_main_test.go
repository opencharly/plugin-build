package build

// test_main_test.go — isolate the CONFIG STACK for every plugin-build test, matching the sdk's own
// loaderkit test_main_test.go. LoadUnified's config stack (sdk/loaderkit/config_stack.go) layers the
// real /etc/charly/charly.yml (the SYSTEM layer) over the in-dir project; without this isolation a
// host that ships a system config makes the stack non-empty for even a project-less temp dir, so
// resolveProjectEnvelope legitimately dials the `loader-bootstrap` HostBuild leg — which the
// nil-executor contract test (resolve_project_word_test.go) relies on NEVER happening. Pointing
// CHARLY_SYSTEM_CONFIG at an absent file reduces the stack to the in-dir project, so the "no
// charly.yml at all → empty envelope, no host leg" contract is what the test actually exercises.
//
// CHARLY_DEPLOY_CONFIG is pointed at an absent file too, for the same reason the sdk test does:
// although the per-host deploy overlay is no longer a stack layer, a test that reaches it through
// its designed per-field merge must still not pick up the operator's real ~/.config/charly/charly.yml.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/opencharly/sdk/loaderkit"
	"github.com/opencharly/spec/spec"
)

func TestMain(m *testing.M) {
	tmp := os.TempDir()
	os.Setenv(loaderkit.SystemConfigEnv, filepath.Join(tmp, "charly-test-absent-system.yml"))
	os.Setenv(spec.DeployConfigEnv, filepath.Join(tmp, "charly-test-absent-user.yml"))
	code := m.Run()
	os.Exit(code)
}
