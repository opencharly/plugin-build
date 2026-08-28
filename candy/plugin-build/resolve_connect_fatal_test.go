package build

import (
	"context"
	"strings"
	"testing"

	"github.com/opencharly/sdk"
	"github.com/opencharly/spec/spec"
	"google.golang.org/grpc"

	pb "github.com/opencharly/spec/proto"
)

// resolve_connect_fatal_test.go — the B12 regression gate for the #326 fix (plugin
// side): a build-time plugin CONNECT failure must be FATAL — the build stops with a
// BuildResolveReply{Error: ...} naming the connect leg — NOT a warning the build
// continues past. Before the fix, hostVoidLeg's error was printed as
// "warning: build-time plugin load: ..." and the build proceeded to a later
// 'no provider registered' failure naming the wrong cause.
//
// The gate drives connectPluginsOrFatal (the extracted step-4 seam) with a fake
// executor whose "buildengine-connect-plugins" leg fails — no loader/scan leg family
// to fake. A revert of the fix (back to the warning behavior) makes the seam return
// done=false with an empty reply, failing this test.

// fakeConnectFatalClient answers the connect leg: failConnect=true makes
// "buildengine-connect-plugins" return an error (the scenario under test);
// failConnect=false makes it succeed (done=false path).
type fakeConnectFatalClient struct {
	pb.ExecutorServiceClient
	failConnect bool
}

// errConnectPlugins is the error the connect leg returns (a plugin candy whose go
// module fails to compile) — the actionable error the fatal reply must carry.
var errConnectPlugins = &errStr{msg: "buildengine-connect-plugins: plugin broken-plugin: go build in /tmp/broken-plugin: exit status 1"}

// errUnexpectedLeg guards the fake against a leg the seam should never dial.
func errUnexpectedLeg(kind string) error {
	return &errStr{msg: "unexpected HostBuild leg in connectPluginsOrFatal: " + kind}
}

// errStr is a minimal error for the fake (no dependency on the package's error type).
type errStr struct{ msg string }

func (e *errStr) Error() string { return e.msg }

func (f *fakeConnectFatalClient) HostBuild(ctx context.Context, in *pb.HostBuildRequest, opts ...grpc.CallOption) (*pb.HostBuildReply, error) {
	if in.GetKind() != "buildengine-connect-plugins" {
		return nil, errUnexpectedLeg(in.GetKind())
	}
	if f.failConnect {
		// The connect failure: a plugin candy whose go module fails to compile.
		return nil, errConnectPlugins
	}
	return &pb.HostBuildReply{ResultJson: nil}, nil
}

func TestConnectPluginsFailureIsFatal(t *testing.T) {
	ex := sdk.NewInProcExecutor(&fakeConnectFatalClient{failConnect: true})
	reply, done := connectPluginsOrFatal(context.Background(), ex, spec.ResolvedProjectRequest{})
	if !done {
		t.Fatal("a build-time plugin CONNECT failure must be FATAL (done=true), not a warning the build continues past (#326)")
	}
	if !strings.Contains(reply.Error, "buildengine-connect-plugins") {
		t.Fatalf("the fatal reply must carry the actionable connect error naming the leg, got %q", reply.Error)
	}
	if !strings.Contains(reply.Error, "broken-plugin") {
		t.Fatalf("the fatal reply must carry the plugin name from the underlying error, got %q", reply.Error)
	}
}

func TestConnectPluginsSuccessIsNotFatal(t *testing.T) {
	ex := sdk.NewInProcExecutor(&fakeConnectFatalClient{failConnect: false})
	reply, done := connectPluginsOrFatal(context.Background(), ex, spec.ResolvedProjectRequest{})
	if done {
		t.Fatal("a successful connect leg must NOT be fatal (done=false)")
	}
	if reply.Error != "" {
		t.Fatalf("a successful connect leg must not set a reply error, got %q", reply.Error)
	}
}
