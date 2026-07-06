package desiredpb

import "testing"

// var _ AgentServer = UnimplementedAgentServer{} is a compile-time check
// that protoc-gen-go-grpc's generated placeholder satisfies the generated
// AgentServer interface — if PushDesired's signature in the .proto ever
// drifts from the interface, this fails to compile.
var _ AgentServer = UnimplementedAgentServer{}

func TestAgent_ServiceDesc(t *testing.T) {
	if got, want := Agent_ServiceDesc.ServiceName, "nanokube.desired.v1.Agent"; got != want {
		t.Errorf("ServiceName = %q, want %q", got, want)
	}

	if got, want := len(Agent_ServiceDesc.Methods), 1; got != want {
		t.Fatalf("len(Methods) = %d, want %d", got, want)
	}
	if got, want := Agent_ServiceDesc.Methods[0].MethodName, "PushDesired"; got != want {
		t.Errorf("Methods[0].MethodName = %q, want %q", got, want)
	}
}
