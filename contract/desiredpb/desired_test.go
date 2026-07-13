package desiredpb

import (
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestDesired_ProtoRoundTrip(t *testing.T) {
	want := &Desired{
		Name:       "abc123",
		BlobSha256: "sha256:cafef00d",
		Blob:       []byte{0x00, 0x01, 0xff, 0x10},
	}

	wire, err := proto.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	got := &Desired{}
	if err := proto.Unmarshal(wire, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.GetName() != want.GetName() {
		t.Errorf("Name = %q, want %q", got.GetName(), want.GetName())
	}
	if got.GetBlobSha256() != want.GetBlobSha256() {
		t.Errorf("BlobSha256 = %q, want %q", got.GetBlobSha256(), want.GetBlobSha256())
	}
	if string(got.GetBlob()) != string(want.GetBlob()) {
		t.Errorf("Blob = %v, want %v", got.GetBlob(), want.GetBlob())
	}
}

func TestDesiredMetadata_JSONRoundTrip(t *testing.T) {
	want := &DesiredMetadata{
		Name:       "abc123",
		BlobSha256: "sha256:cafef00d",
	}

	data, err := protojson.Marshal(want)
	if err != nil {
		t.Fatalf("protojson.Marshal: %v", err)
	}

	got := &DesiredMetadata{}
	if err := protojson.Unmarshal(data, got); err != nil {
		t.Fatalf("protojson.Unmarshal: %v", err)
	}

	if got.GetName() != want.GetName() {
		t.Errorf("Name = %q, want %q", got.GetName(), want.GetName())
	}
	if got.GetBlobSha256() != want.GetBlobSha256() {
		t.Errorf("BlobSha256 = %q, want %q", got.GetBlobSha256(), want.GetBlobSha256())
	}
}
