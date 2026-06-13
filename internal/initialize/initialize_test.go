package initialize

import (
	"testing"

	"github.com/MatchaScript/nanokube/internal/layouttest"
	"github.com/MatchaScript/nanokube/internal/state"
)

func TestWriteFirstBootState_RecordsControlPlaneRole(t *testing.T) {
	l := layouttest.New(t)
	if err := writeFirstBootState(l, "v1.35.0", false); err != nil {
		t.Fatalf("writeFirstBootState: %v", err)
	}
	lb, had, err := state.ReadLastBoot(l)
	if err != nil || !had {
		t.Fatalf("ReadLastBoot: err=%v had=%v", err, had)
	}
	if lb.Role != state.RoleControlPlane {
		t.Errorf("role: got %q, want %q", lb.Role, state.RoleControlPlane)
	}
}
