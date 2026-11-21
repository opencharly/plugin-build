package build

import (
	"context"
	"encoding/json"
	"testing"

	"google.golang.org/grpc"

	"github.com/opencharly/sdk"
	pb "github.com/opencharly/spec/proto"
	"github.com/opencharly/spec/spec"
)

// owned_content_test.go — ownedContentSource, the render-prep's way to reach the content kinds
// (opencharly/sdk#598): the kind words come from the host's word-set leg (content_kinds, a registry
// fact), and asking one is a peer InvokeProvider of kind:<word> ops.OpContent carrying the request
// verbatim. The fake executor answers exactly those two RPCs and records what crossed the wire.
type fakeContentExecutor struct {
	pb.ExecutorServiceClient
	hostLegs []string
	invoked  []*pb.InvokeProviderRequest
}

func (f *fakeContentExecutor) HostBuild(_ context.Context, in *pb.HostBuildRequest, _ ...grpc.CallOption) (*pb.HostBuildReply, error) {
	f.hostLegs = append(f.hostLegs, in.GetKind())
	res, err := json.Marshal(spec.ValidateWordSetsReply{ContentKinds: []string{"skill"}})
	return &pb.HostBuildReply{ResultJson: res}, err
}

func (f *fakeContentExecutor) InvokeProvider(_ context.Context, in *pb.InvokeProviderRequest, _ ...grpc.CallOption) (*pb.InvokeReply, error) {
	f.invoked = append(f.invoked, in)
	res, err := json.Marshal(spec.OwnedContentReply{Files: []spec.ContentFile{{Source: "/r/a", Path: "a"}}})
	return &pb.InvokeReply{ResultJson: res}, err
}

func TestOwnedContentSource_KindsFromTheHostAskThroughInvokeProvider(t *testing.T) {
	fake := &fakeContentExecutor{}
	src, err := ownedContentSource(context.Background(), sdk.NewInProcExecutor(fake))
	if err != nil {
		t.Fatalf("ownedContentSource: %v", err)
	}
	if len(fake.hostLegs) != 1 || fake.hostLegs[0] != validateWordSetsLeg {
		t.Fatalf("host legs = %v, want exactly the word-set leg", fake.hostLegs)
	}
	if len(src.Kinds) != 1 || src.Kinds[0] != "skill" {
		t.Fatalf("kinds = %v, want the host's content_kinds", src.Kinds)
	}
	reply, err := src.Ask("skill", spec.OwnedContentRequest{Box: "arch", Home: "/home/user"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(reply.Files) != 1 || reply.Files[0].Path != "a" {
		t.Fatalf("reply = %+v, want the kind's reply decoded", reply)
	}
	if len(fake.invoked) != 1 {
		t.Fatalf("InvokeProvider called %d times, want once", len(fake.invoked))
	}
	call := fake.invoked[0]
	var sent spec.OwnedContentRequest
	if err := json.Unmarshal(call.GetParamsJson(), &sent); err != nil {
		t.Fatal(err)
	}
	if call.GetClass() != "kind" || call.GetReserved() != "skill" || call.GetOp() != sdk.OpContent || sent.Box != "arch" {
		t.Fatalf("invoked %s:%s %s with %+v, want kind:skill content with the request verbatim", call.GetClass(), call.GetReserved(), call.GetOp(), sent)
	}
}
