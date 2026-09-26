package build

import (
	"context"
	"encoding/json"
	"testing"

	"google.golang.org/grpc"

	"github.com/opencharly/sdk"
	"github.com/opencharly/spec/proto"
	"github.com/opencharly/spec/spec"
)

// validate_leg_test.go — proves validateProjectLeg dispatches the pre-build validation
// GATE by its full capability IDENTITY: `command:validate:box`. A nested command is never
// reachable by its bare word (the provider registry keys it `command:<word>:<parent>`), so
// the peer dispatch MUST name the parent. This test FAILS on the pre-change code, whose call
// passed no CommandParent.
//
// The stub embeds the generated ExecutorServiceClient interface (satisfying the whole shape)
// and overrides InvokeProvider to (a) RECORD the request and (b) return an error-severity
// diagnostic, so the assertion covers both the dispatch identity AND the diagnostics decode.
type captureExecClient struct {
	proto.ExecutorServiceClient
	got *proto.InvokeProviderRequest
}

func (c *captureExecClient) InvokeProvider(_ context.Context, in *proto.InvokeProviderRequest, _ ...grpc.CallOption) (*proto.InvokeReply, error) {
	c.got = in
	diags := spec.Diagnostics{Items: []spec.Diagnostic{{Severity: "error", Message: "boom"}}}
	body, _ := json.Marshal(diags)
	return &proto.InvokeReply{ResultJson: body}, nil
}

func TestValidateProjectLeg_DispatchesNestedCommandIdentity(t *testing.T) {
	cap := &captureExecClient{}
	ex := sdk.NewInProcExecutor(cap)

	err := validateProjectLeg(context.Background(), ex, spec.ResolvedProjectRequest{Dir: "/tmp/x"})
	// The stub returns an error-severity diagnostic, so the leg returns the rendered verdict.
	if err == nil {
		t.Fatal("validateProjectLeg returned nil; the error-severity diagnostic must render a verdict")
	}
	if cap.got == nil {
		t.Fatal("validateProjectLeg never dispatched InvokeProvider")
	}
	if cap.got.GetClass() != "command" || cap.got.GetReserved() != "validate" {
		t.Fatalf("InvokeProvider target = %s:%s, want command:validate", cap.got.GetClass(), cap.got.GetReserved())
	}
	if cap.got.GetCommandParent() != "box" {
		t.Fatalf("InvokeProvider command_parent = %q, want %q — a nested command must name its parent (command:validate:box)", cap.got.GetCommandParent(), "box")
	}
}
