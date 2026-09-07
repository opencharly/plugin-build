package build

// resolve_project_substrate_test.go — the Cutover C settled-contract repro for the
// build:project envelope (spec v0.2026249.2215 + sdk v0.2026249.2239): the pod substrate
// must parse, validate, and SURVIVE the envelope projection for the canonical imageless
// agent_provisioned iterate-entity shape (agent_provisioned: true + an iterate: block and
// NO image:), not only the legacy imageful spelling.
//
// The distro-arch check-agent-live repro: under the pre-wave embedded contract, the
// substrate branch of the loaderkit parse classified the iterate: block as an IN-SUBSTRATE
// member purely because its value mapping carries the kind-word key agent: — parseNode
// ("iterate") then hard-failed on the scalar sandbox: and the whole envelope resolve
// aborted, so downstream consumers saw "no entity check-agent-live". sdk #221/#225 fixed
// the parse (a declared #Deploy field is DATA — its value is never looked inside) and spec
// #107 added the DeployDeclaredFields channel + the Threaded fields this test threads.
//
// This file MIRRORS sdk loaderkit/parse_guard_test.go's TestParse_SubstrateDeclaredIterateStaysData
// at the plugin-build level, over the SAME exported primitives the build:project envelope
// resolve runs (resolveProjectEnvelope → loaderkit.LoadUnifiedViaExecutor → ParseDoc →
// per-entity CUE gates → spec.ValidateDeploymentTree on the merged Deploy):
//
//   1. the imageless agent_provisioned pod + iterate block parses and survives
//     (the CUE gates accept it; the node body + the deploy-level sibling classify);
//   2. an imageful pod still parses (the legacy spelling is unchanged);
//   3. the Deploy gate (spec.ValidateDeploymentTree) keeps the imageless
//     agent_provisioned node in the envelope Deploy map and — discriminator — still
//     rejects a plain imageless pod without the flag, plus the position-derived member
//     tree (DeployLevelMembers/InSubstrateMembers/MemberByName/HasMembers) classifies
//     the fedora migrated deploy-level sibling spelling.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/opencharly/sdk/loaderkit"
	"github.com/opencharly/spec/spec"
	"gopkg.in/yaml.v3"
)

// projectThreaded models the LIVE host threading for the canonical charly-cli
// iterate-entity bed shape — the registry-derived snapshot the loader-threaded leg feeds
// (sdk loaderkit/parse_guard_test.go's livePodThreaded, mirrored verbatim): pod is BOTH a
// deploy substrate AND a structural kind, agent is a threaded kind word colliding with
// values inside a declared #Deploy field's body, and the deploy-declared-fields channel
// carries the #Deploy body field names the REGISTERED substrate schema declares. Host-fed
// DATA, not the mechanism's own vocabulary.
var projectThreaded = spec.Threaded{
	Kinds:            map[string]bool{"pod": true, "agent": true, "check": true},
	DeploySubstrates: map[string]bool{"pod": true, "vm": true},
	StructuralKinds:  map[string]bool{"pod": true, "vm": true, "agent": true},
	DeployDeclaredFields: map[string]map[string]bool{
		"pod": {
			"from": true, "image": true, "env": true, "disposable": true,
			"plan": true, "iterate": true, "record": true, "instrument": true,
			"cpus": true, "ram": true, "disk_size": true, "snapshot": true,
			"update_gate": true, "agent_provisioned": true,
		},
	},
}

// docParserAdapter adapts loaderkit.ParseDoc to the spec.DocParser seam
// ValidateNodeFormSteps takes (the same adapter shape the load chain wires).
type docParserAdapter struct{}

func (docParserAdapter) ParseDoc(doc *yaml.Node, t spec.Threaded) (map[string]*yaml.Node, spec.ParsedProject, error) {
	return loaderkit.ParseDoc(doc, t)
}

// docNode parses raw YAML into the document node ParseDoc consumes.
func docNode(t *testing.T, s string) *yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(s), &n); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	return &n
}

// bodyMap decodes a parsed node's opaque body JSON for assertions.
func bodyMap(t *testing.T, body json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body json: %v", err)
	}
	return m
}

const imagelessAgentProvisionedDoc = `
check-agent-live:
  pod:
    agent_provisioned: true
    iterate:
      sandbox: check-agent-pod
      agent: [check-agent-live-claude]
      plateau_iteration: 1
      prompt: Reply with one short acknowledgement.
      note: false
      env: {}
  watcher:
    pod:
      image: watcher-img
`

// TestBuildProjectEnvelope_ImagelessAgentProvisionedPodSurvives is the semantic repro: the
// NEW canonical imageless agent_provisioned iterate-entity pod parses, passes BOTH CUE
// gates the load chain runs (the per-entity #NodeDoc structural gate and the step-typing
// gate), and survives with its iterate data intact plus the deploy-level sibling watcher.
// Under the pre-wave contract this shape aborted the envelope resolve, so every downstream
// build:project consumer saw "no entity check-agent-live".
func TestBuildProjectEnvelope_ImagelessAgentProvisionedPodSurvives(t *testing.T) {
	if err := loaderkit.ValidateNodeDocCUE("repro-imageless", []byte(imagelessAgentProvisionedDoc)); err != nil {
		t.Fatalf("ValidateNodeDocCUE: the settled pod schema must accept the imageless agent_provisioned shape: %v", err)
	}
	if err := loaderkit.ValidateNodeFormSteps("repro-imageless", []byte(imagelessAgentProvisionedDoc), projectThreaded, docParserAdapter{}); err != nil {
		t.Fatalf("ValidateNodeFormSteps: %v", err)
	}

	_, pp, err := loaderkit.ParseDoc(docNode(t, imagelessAgentProvisionedDoc), projectThreaded)
	if err != nil {
		t.Fatalf("ParseDoc: %v", err)
	}
	if len(pp.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1 — the imageless pod must survive into the envelope", len(pp.Nodes))
	}
	pn := pp.Nodes[0]
	if pn.Disc != "pod" {
		t.Fatalf("disc = %q, want pod", pn.Disc)
	}
	body := bodyMap(t, pn.Body)
	if _, hasImage := body["image"]; hasImage {
		t.Fatalf("body.image = %v, want ABSENT (the imageless canonical shape)", body["image"])
	}
	if body["agent_provisioned"] != true {
		t.Fatalf("body.agent_provisioned = %v, want true", body["agent_provisioned"])
	}
	it, ok := body["iterate"].(map[string]any)
	if !ok {
		t.Fatalf("body.iterate = %v (%T), want the iterate mapping intact", body["iterate"], body["iterate"])
	}
	if it["sandbox"] != "check-agent-pod" || it["plateau_iteration"] != float64(1) || it["note"] != false {
		t.Fatalf("iterate data lost: %v", it)
	}
	agents, ok := it["agent"].([]any)
	if !ok || len(agents) != 1 || agents[0] != "check-agent-live-claude" {
		t.Fatalf("iterate.agent = %v, want the one-word list", it["agent"])
	}
	if _, ok := it["env"].(map[string]any); !ok {
		t.Fatalf("iterate.env = %v, want the empty mapping", it["env"])
	}
	// iterate is DATA, never a member: the only member is the deploy-level sibling.
	if len(pn.Children) != 1 || pn.Children[0].Name != "watcher" {
		t.Fatalf("children = %+v, want just the deploy-level sibling watcher", pn.Children)
	}
}

// TestBuildProjectEnvelope_ImagefulPodStillParses mirrors sdk's
// TestParse_SubstrateDeclaredIterateStaysData verbatim: the LEGACY imageful spelling keeps
// parsing — the channel excludes declared fields from the member scan, nothing else moved.
func TestBuildProjectEnvelope_ImagefulPodStillParses(t *testing.T) {
	doc := `
check-agent-live:
  pod:
    image: sandbox-img
    iterate:
      sandbox: check-agent-pod
      agent: [check-agent-live-claude]
      plateau_iteration: 1
      prompt: Reply with one short acknowledgement.
      note: false
      env: {}
  watcher:
    pod:
      image: watcher-img
`
	if err := loaderkit.ValidateNodeDocCUE("repro-imageful", []byte(doc)); err != nil {
		t.Fatalf("ValidateNodeDocCUE: %v", err)
	}
	if err := loaderkit.ValidateNodeFormSteps("repro-imageful", []byte(doc), projectThreaded, docParserAdapter{}); err != nil {
		t.Fatalf("ValidateNodeFormSteps: %v", err)
	}
	_, pp, err := loaderkit.ParseDoc(docNode(t, doc), projectThreaded)
	if err != nil {
		t.Fatalf("ParseDoc: %v", err)
	}
	if len(pp.Nodes) != 1 || pp.Nodes[0].Disc != "pod" {
		t.Fatalf("nodes = %+v, want one pod node", pp.Nodes)
	}
	pn := pp.Nodes[0]
	body := bodyMap(t, pn.Body)
	if body["image"] != "sandbox-img" {
		t.Fatalf("body.image = %v, want sandbox-img", body["image"])
	}
	if len(pn.Children) != 1 || pn.Children[0].Name != "watcher" {
		t.Fatalf("children = %+v, want just the deploy-level sibling watcher", pn.Children)
	}
}

// TestBuildProjectEnvelope_FleetGateAndMemberTree proves the envelope Deploy-map gate and
// the position-derived member tree over the settled contracts:
//   - the imageless agent_provisioned pod STAYS in the Deploy (the ValidateDeployRequiresBox
//     exemption) — the exact gate that used to drop it;
//   - the discriminator: the same imageless node WITHOUT the flag is rejected with the
//     box-required error (the gate did not go away — the exemption is the only change);
//   - the fedora migrated deploy-level sibling spelling classifies via the member tree
//     consult sites (DeployLevelMembers/InSubstrateMembers/MemberByName/HasMembers).
func TestBuildProjectEnvelope_FleetGateAndMemberTree(t *testing.T) {
	t.Run("agent-provisioned-stays-in-deploy", func(t *testing.T) {
		deploy := map[string]spec.Deploy{
			"check-agent-live": {
				Target:           "pod",
				AgentProvisioned: true,
				Iterate:          &spec.Iterate{Sandbox: "check-agent-pod"},
				Member: []spec.Member{
					{Name: "watcher", Position: spec.PositionDeployLevel, Node: &spec.Deploy{Target: "pod", Image: "watcher-img"}},
				},
			},
		}
		if err := spec.ValidateDeploymentTree(deploy); err != nil {
			t.Fatalf("ValidateDeploymentTree: the imageless agent_provisioned pod must stay in the envelope Deploy map: %v", err)
		}
	})

	t.Run("plain-imageless-pod-still-rejected", func(t *testing.T) {
		deploy := map[string]spec.Deploy{
			"bare-pod": {Target: "pod"},
		}
		err := spec.ValidateDeploymentTree(deploy)
		if err == nil {
			t.Fatal("ValidateDeploymentTree: want the box-required rejection for a plain imageless pod")
		}
		if want := "lacks required"; !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to contain %q", err, want)
		}
	})

	t.Run("deploy-level-sibling-member-tree", func(t *testing.T) {
		// The fedora migrated spelling: a deploy-level chrome: sibling NEXT TO the pod:,
		// folded as a MEMBER beside the parent — alongside-vs-deploy-into is DERIVED from
		// the member's authored Position (the fold stamps it from depth alone).
		node := &spec.Deploy{
			Target: "pod",
			Image:  "parent-img",
			Member: []spec.Member{
				{Name: "watcher", Position: spec.PositionDeployLevel, Node: &spec.Deploy{Target: "pod", Image: "watcher-img"}},
				{Name: "chrome", Position: spec.PositionDeployLevel, Node: &spec.Deploy{Target: "pod", Image: "chrome-img"}},
				{Name: "innersidecar", Position: spec.PositionInSubstrate, Node: &spec.Deploy{Target: "pod", Image: "inner-img"}},
			},
		}
		if !node.HasMembers() {
			t.Fatal("HasMembers = false, want true")
		}
		deployLevel := node.DeployLevelMembers()
		if len(deployLevel) != 2 || deployLevel[0].Name != "watcher" || deployLevel[1].Name != "chrome" {
			t.Fatalf("DeployLevelMembers = %+v, want [watcher chrome] in authored order", deployLevel)
		}
		inSubstrate := node.InSubstrateMembers()
		if len(inSubstrate) != 1 || inSubstrate[0].Name != "innersidecar" {
			t.Fatalf("InSubstrateMembers = %+v, want [innersidecar]", inSubstrate)
		}
		chrome := node.MemberByName("chrome")
		if chrome == nil || !chrome.Alongside() || chrome.InSubstrate() {
			t.Fatalf("MemberByName(chrome) = %+v, want the alongside deploy-level member", chrome)
		}
		if chrome.Node.Image != "chrome-img" {
			t.Fatalf("chrome member node image = %q, want chrome-img", chrome.Node.Image)
		}
		if m := node.MemberByName("absent"); m != nil {
			t.Fatalf("MemberByName(absent) = %+v, want nil", m)
		}
	})
}
