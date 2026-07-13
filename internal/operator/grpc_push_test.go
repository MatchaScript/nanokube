package operator

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/MatchaScript/nanokube/contract/desiredpb"
)

// fakeAgentServer is a minimal desiredpb.AgentServer test double: it
// records the last Desired it received and returns either a canned
// PushDesiredResponse or a canned gRPC error, so tests can assert what
// NewGRPCPush actually sent without a real nanokube-agent process.
type fakeAgentServer struct {
	desiredpb.UnimplementedAgentServer

	received *desiredpb.Desired
	respName string
	err      error
}

func (f *fakeAgentServer) PushDesired(_ context.Context, in *desiredpb.Desired) (*desiredpb.PushDesiredResponse, error) {
	f.received = in
	if f.err != nil {
		return nil, f.err
	}
	return &desiredpb.PushDesiredResponse{DesiredName: f.respName}, nil
}

// startFakeAgent listens on a real loopback TCP port (127.0.0.1:0, OS
// picks a free port) and serves srv until the test ends, returning the
// address NewGRPCPush should dial.
func startFakeAgent(t *testing.T, srv *fakeAgentServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := grpc.NewServer()
	desiredpb.RegisterAgentServer(s, srv)
	go func() {
		_ = s.Serve(lis)
	}()
	t.Cleanup(s.Stop)
	return lis.Addr().String()
}

func TestNewGRPCPush_SuccessSendsExactDesired(t *testing.T) {
	fake := &fakeAgentServer{respName: "v1-name"}
	addr := startFakeAgent(t, fake)

	dir := t.TempDir()
	blob := []byte("confext-blob-bytes")
	rawPath := filepath.Join(dir, "v1-name.raw")
	if err := os.WriteFile(rawPath, blob, 0o644); err != nil {
		t.Fatalf("write blob: %v", err)
	}

	push := NewGRPCPush(addr)
	meta := &desiredpb.DesiredMetadata{
		Name:       "v1-name",
		BlobSha256: "abc123",
	}
	if err := push(context.Background(), meta, rawPath); err != nil {
		t.Fatalf("push: %v", err)
	}

	if fake.received == nil {
		t.Fatal("agent never received a PushDesired call")
	}
	if fake.received.Name != meta.Name {
		t.Errorf("received.Name = %q, want %q", fake.received.Name, meta.Name)
	}
	if fake.received.BlobSha256 != meta.BlobSha256 {
		t.Errorf("received.BlobSha256 = %q, want %q", fake.received.BlobSha256, meta.BlobSha256)
	}
	if string(fake.received.Blob) != string(blob) {
		t.Errorf("received.Blob = %q, want %q", fake.received.Blob, blob)
	}
}

func TestNewGRPCPush_AgentErrorSurfaces(t *testing.T) {
	fake := &fakeAgentServer{err: status.Error(codes.InvalidArgument, "blob checksum mismatch: want x, got y")}
	addr := startFakeAgent(t, fake)

	dir := t.TempDir()
	rawPath := filepath.Join(dir, "name.raw")
	if err := os.WriteFile(rawPath, []byte("blob"), 0o644); err != nil {
		t.Fatalf("write blob: %v", err)
	}

	push := NewGRPCPush(addr)
	meta := &desiredpb.DesiredMetadata{Name: "name", BlobSha256: "bad"}
	err := push(context.Background(), meta, rawPath)
	if err == nil {
		t.Fatal("push: want non-nil error, got nil")
	}
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Errorf("status.Code(err) = %v, want %v (gRPC status must survive the wrap)", got, codes.InvalidArgument)
	}
}

func TestNewGRPCPush_MissingBlobSkipsPushWithoutError(t *testing.T) {
	fake := &fakeAgentServer{}
	addr := startFakeAgent(t, fake)

	dir := t.TempDir()
	rawPath := filepath.Join(dir, "missing.raw") // deliberately never written

	push := NewGRPCPush(addr)
	meta := &desiredpb.DesiredMetadata{Name: "missing", BlobSha256: buildSkippedSentinel}
	if err := push(context.Background(), meta, rawPath); err != nil {
		t.Fatalf("push: %v, want nil (a missing blob should skip the push, not error)", err)
	}
	if fake.received != nil {
		t.Errorf("agent received a PushDesired call, want none (push should have been skipped before dialing)")
	}
}
