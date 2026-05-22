package certs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckLeavesReportsValidExpiry(t *testing.T) {
	cfg := testConfig()
	layout := testLayout(t)
	signer := NewSigner(cfg, layout, "node-1")
	if err := signer.EnsureAll(); err != nil {
		t.Fatal(err)
	}

	report, err := CheckLeaves(cfg, layout, "node-1")
	if err != nil {
		t.Fatalf("CheckLeaves: %v", err)
	}
	api, ok := report[LeafAPIServer]
	if !ok {
		t.Fatal("LeafAPIServer not in report")
	}
	if api.NotFound {
		t.Error("apiserver.crt reported as NotFound after EnsureAll")
	}
	// Default LeafValidityDays is 365; allow generous window for clock jitter.
	if api.Remaining < 360*24*time.Hour || api.Remaining > 365*24*time.Hour {
		t.Errorf("apiserver Remaining=%v, expected ~365d", api.Remaining)
	}
}

func TestCheckLeavesReportsNotFoundForAbsentSuperAdmin(t *testing.T) {
	cfg := testConfig()
	layout := testLayout(t)
	signer := NewSigner(cfg, layout, "node-1")
	if err := signer.EnsureAll(); err != nil {
		t.Fatal(err)
	}
	// EnsureAll does not produce super-admin.conf — mirroring the
	// production state during lifecycle.Boot.
	report, err := CheckLeaves(cfg, layout, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	sa, ok := report[LeafSuperAdminConf]
	if !ok {
		t.Fatal("LeafSuperAdminConf not in report")
	}
	if !sa.NotFound {
		t.Errorf("super-admin.conf should be NotFound, got Remaining=%v", sa.Remaining)
	}
	saPath := filepath.Join(layout.KubeconfigDir, "super-admin.conf")
	if _, err := os.Stat(saPath); err == nil {
		t.Fatalf("test precondition violated: super-admin.conf exists at %s", saPath)
	}
}

// With LeafValidityDays = 1, every leaf trips NeedsRotation — exactly
// the situation lifecycle.Boot must detect.
func TestCheckLeavesFlagsExpiringLeavesAsBelowThreshold(t *testing.T) {
	cfg := testConfig()
	cfg.Spec.Certificates.LeafValidityDays = 1
	layout := testLayout(t)
	signer := NewSigner(cfg, layout, "node-1")
	if err := signer.EnsureAll(); err != nil {
		t.Fatal(err)
	}

	report, err := CheckLeaves(cfg, layout, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, leaf := range AllLeaves() {
		if leaf == LeafSuperAdminConf {
			continue // not produced by EnsureAll
		}
		exp := report[leaf]
		if exp.NotFound {
			t.Errorf("%s reported NotFound; expected present", leaf)
			continue
		}
		if !NeedsRotation(exp.Cert) {
			t.Errorf("%s NeedsRotation=false (Remaining=%v); expected true with 1d validity",
				leaf, exp.Remaining)
		}
	}
}
