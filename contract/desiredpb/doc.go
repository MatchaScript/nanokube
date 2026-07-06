// Package desiredpb holds the generated Go bindings for
// contract/desired.proto — the cross-language contract for the desired
// document exchanged between the operator (Go) and the agent (Rust).
//
// Regenerate after editing contract/desired.proto by running, from the
// repo root:
//
//	mise exec -- sh -c 'protoc --proto_path=contract --go_out=. --go_opt=module=github.com/MatchaScript/nanokube --go-grpc_out=. --go-grpc_opt=module=github.com/MatchaScript/nanokube contract/desired.proto'
package desiredpb

//go:generate sh -c "cd ../.. && protoc --proto_path=contract --go_out=. --go_opt=module=github.com/MatchaScript/nanokube --go-grpc_out=. --go-grpc_opt=module=github.com/MatchaScript/nanokube contract/desired.proto"
