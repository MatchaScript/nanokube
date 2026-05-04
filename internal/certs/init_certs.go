package certs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	v1alpha1 "github.com/MatchaScript/nanokube/internal/apis/bootstrap/v1alpha1"
)

// Init performs first-time PKI provisioning for a fresh node. It copies
// any operator-supplied CA pairs from layout.OperatorDir into
// layout.PKIDir, then delegates to Signer.EnsureAll which fills in the
// remaining (self-signed) CAs and every leaf via the kubeadm phase.
//
// Called exclusively from initialize.Run. lifecycle.Boot must NOT
// invoke Init: by then PKIDir is the build artifact and reseeding
// would silently change identities under a running cluster.
func Init(cfg *v1alpha1.NanoKubeConfig, layout Layout, nodeName string) error {
	if err := seedOperatorCAs(layout); err != nil {
		return err
	}
	return NewSigner(cfg, layout, nodeName).EnsureAll()
}

// seededCAs lists the (operatorRelative, pkiRelative) base names of CA
// pairs nanokube recognises in OperatorDir. The relative paths under
// OperatorDir mirror those under PKIDir, so a copy is a straight
// rename — no path translation needed.
var seededCAs = []struct {
	dir  string // subdir under OperatorDir / PKIDir ("" or "etcd")
	base string // file basename without extension
}{
	{"", "ca"},
	{"etcd", "ca"},
	{"", "front-proxy-ca"},
}

// seedOperatorCAs walks the recognised CA names and copies any
// (.crt + .key) pair found under OperatorDir into PKIDir. Partial
// pairs (only crt or only key) are rejected so an operator typo does
// not silently fall back to self-signing.
func seedOperatorCAs(layout Layout) error {
	for _, ca := range seededCAs {
		srcDir := filepath.Join(layout.OperatorDir, ca.dir)
		dstDir := filepath.Join(layout.PKIDir, ca.dir)
		crtSrc := filepath.Join(srcDir, ca.base+".crt")
		keySrc := filepath.Join(srcDir, ca.base+".key")

		crtExists, err := pathExists(crtSrc)
		if err != nil {
			return err
		}
		keyExists, err := pathExists(keySrc)
		if err != nil {
			return err
		}
		switch {
		case !crtExists && !keyExists:
			continue // nothing to seed for this CA → fall through to self-sign
		case crtExists != keyExists:
			return fmt.Errorf("operator CA %s/%s: both .crt and .key must be supplied (got crt=%v, key=%v)",
				ca.dir, ca.base, crtExists, keyExists)
		}

		if err := os.MkdirAll(dstDir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dstDir, err)
		}
		if err := copyFile(crtSrc, filepath.Join(dstDir, ca.base+".crt"), 0o644); err != nil {
			return err
		}
		if err := copyFile(keySrc, filepath.Join(dstDir, ca.base+".key"), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func pathExists(p string) (bool, error) {
	_, err := os.Stat(p)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copy %s -> %s: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", dst, err)
	}
	return nil
}
