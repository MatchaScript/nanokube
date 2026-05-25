package certs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCheckLeavesReportsValidExpiry(t *testing.T) {
	cfg := testConfig(t)
	layout := testLayout(t)
	signer := NewSigner(cfg, layout)
	if err := signer.EnsureAll(); err != nil {
		t.Fatal(err)
	}

	report, err := CheckLeaves(cfg, layout)
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
	// Default kubeadm leaf validity is 365 days; allow a generous window
	// for clock jitter.
	if api.Remaining < 360*24*time.Hour || api.Remaining > 365*24*time.Hour {
		t.Errorf("apiserver Remaining=%v, expected ~365d", api.Remaining)
	}
}

func TestCheckLeavesReportsNotFoundForAbsentSuperAdmin(t *testing.T) {
	cfg := testConfig(t)
	layout := testLayout(t)
	signer := NewSigner(cfg, layout)
	if err := signer.EnsureAll(); err != nil {
		t.Fatal(err)
	}
	// EnsureAll does not produce super-admin.conf — mirroring the
	// production state during boot.Run.
	report, err := CheckLeaves(cfg, layout)
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
	saPath := filepath.Join(layout.KubernetesDir, "super-admin.conf")
	if _, err := os.Stat(saPath); err == nil {
		t.Fatalf("test precondition violated: super-admin.conf exists at %s", saPath)
	}
}

// With a 1-day leaf validity, every leaf trips NeedsRotation — exactly
// the situation boot.Run must detect.
func TestCheckLeavesFlagsExpiringLeavesAsBelowThreshold(t *testing.T) {
	cfg := testConfig(t)
	cfg.CertificateValidityPeriod = &metav1.Duration{Duration: 24 * time.Hour}
	layout := testLayout(t)
	signer := NewSigner(cfg, layout)
	if err := signer.EnsureAll(); err != nil {
		t.Fatal(err)
	}

	report, err := CheckLeaves(cfg, layout)
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
