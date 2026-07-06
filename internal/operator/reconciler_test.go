package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubeadmconfig "k8s.io/kubernetes/cmd/kubeadm/app/util/config"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/MatchaScript/nanokube/contract/desiredpb"
	"github.com/MatchaScript/nanokube/internal/ddi"
	"github.com/MatchaScript/nanokube/internal/render"
)

func TestMain(m *testing.M) {
	// Quiets controller-runtime's "log.SetLogger(...) was never called"
	// warning; the log output itself is irrelevant to these tests.
	logf.SetLogger(zap.New(zap.UseDevMode(true)))
	os.Exit(m.Run())
}

const (
	testName      = "nanokube-desired-input"
	testNamespace = "default"
	testDigest1   = "sha256:deadbeef1"
	testDigest2   = "sha256:deadbeef2"
)

func testRequest() ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: testName, Namespace: testNamespace}}
}

// recordingPush is a PushFunc stub that records every call it receives,
// letting tests assert both "was push invoked at all" (the idempotency
// question) and "with what" (the content question) without depending
// on NewLocalPush's own file-writing behaviour.
type recordingPush struct {
	calls []pushCall
}

type pushCall struct {
	meta    *desiredpb.DesiredMetadata
	rawPath string
}

func (p *recordingPush) fn() PushFunc {
	return func(_ context.Context, meta *desiredpb.DesiredMetadata, rawPath string) error {
		p.calls = append(p.calls, pushCall{meta: meta, rawPath: rawPath})
		return nil
	}
}

// renderedName computes the same content-hash name the reconciler
// itself derives for its (currently invariant) render input, so tests
// can pre-populate outputDir at the exact path Reconcile will look at.
func renderedName(t *testing.T) string {
	t.Helper()
	cfg, err := kubeadmconfig.DefaultedStaticInitConfiguration()
	if err != nil {
		t.Fatalf("DefaultedStaticInitConfiguration: %v", err)
	}
	kubeletFile, err := render.KubeletConfig(cfg)
	if err != nil {
		t.Fatalf("render.KubeletConfig: %v", err)
	}
	return render.Desired{Files: []render.File{kubeletFile}}.Name()
}

func TestReconcile_ConfigMapNotFound(t *testing.T) {
	push := &recordingPush{}
	r := &Reconciler{
		Client:             fake.NewClientBuilder().Build(),
		ConfigMapName:      testName,
		ConfigMapNamespace: testNamespace,
		OutputDir:          t.TempDir(),
		Push:               push.fn(),
	}

	res, err := r.Reconcile(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res != (ctrl.Result{}) {
		t.Errorf("Result = %+v, want zero value", res)
	}
	if len(push.calls) != 0 {
		t.Errorf("push called %d times, want 0 (configmap does not exist)", len(push.calls))
	}
}

func TestReconcile_IgnoresUnrelatedObject(t *testing.T) {
	push := &recordingPush{}
	// An object exists, but under a different name/namespace than the
	// Reconciler watches -- proves the name/namespace filter, not just
	// "not found", is what gates work.
	other := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "some-other-configmap", Namespace: "kube-system"},
		Data:       map[string]string{TargetImageDigestKey: testDigest1},
	}
	r := &Reconciler{
		Client:             fake.NewClientBuilder().WithObjects(other).Build(),
		ConfigMapName:      testName,
		ConfigMapNamespace: testNamespace,
		OutputDir:          t.TempDir(),
		Push:               push.fn(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "some-other-configmap", Namespace: "kube-system"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(push.calls) != 0 {
		t.Errorf("push called %d times, want 0 (unrelated object)", len(push.calls))
	}
}

func TestReconcile_NoDigestKey(t *testing.T) {
	push := &recordingPush{}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace},
		Data:       map[string]string{"unrelated": "value"},
	}
	r := &Reconciler{
		Client:             fake.NewClientBuilder().WithObjects(cm).Build(),
		ConfigMapName:      testName,
		ConfigMapNamespace: testNamespace,
		OutputDir:          t.TempDir(),
		Push:               push.fn(),
	}

	if _, err := r.Reconcile(context.Background(), testRequest()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(push.calls) != 0 {
		t.Errorf("push called %d times, want 0 (no targetImageDigest key)", len(push.calls))
	}
}

func TestReconcile_RendersAndPushes(t *testing.T) {
	push := &recordingPush{}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace},
		Data:       map[string]string{TargetImageDigestKey: testDigest1},
	}
	r := &Reconciler{
		Client:             fake.NewClientBuilder().WithObjects(cm).Build(),
		ConfigMapName:      testName,
		ConfigMapNamespace: testNamespace,
		OutputDir:          t.TempDir(),
		Push:               push.fn(),
	}

	if _, err := r.Reconcile(context.Background(), testRequest()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(push.calls) != 1 {
		t.Fatalf("push called %d times, want 1", len(push.calls))
	}
	got := push.calls[0].meta
	if got.Name == "" {
		t.Error("meta.Name is empty")
	}
	if got.TargetImageDigest != testDigest1 {
		t.Errorf("meta.TargetImageDigest = %q, want %q", got.TargetImageDigest, testDigest1)
	}
	// This dev host lacks mkfs.erofs (confirmed separately), so the
	// realistic outcome here is the documented sentinel. If some other
	// host in CI *does* have the DDI toolchain, a real hex sha256 is
	// just as acceptable -- either way blobSha256 must be one of the
	// two, never empty or garbage.
	if got.BlobSha256 != buildSkippedSentinel {
		if _, err := hex.DecodeString(got.BlobSha256); err != nil || len(got.BlobSha256) != 64 {
			t.Errorf("meta.BlobSha256 = %q, want sentinel or a 64-hex-char sha256", got.BlobSha256)
		}
	}
}

func TestReconcile_IdempotentOnUnchangedInput(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace},
		Data:       map[string]string{TargetImageDigestKey: testDigest1},
	}
	outputDir := t.TempDir()
	r := &Reconciler{
		Client:             fake.NewClientBuilder().WithObjects(cm).Build(),
		ConfigMapName:      testName,
		ConfigMapNamespace: testNamespace,
		OutputDir:          outputDir,
		// The idempotency check reads the sidecar NewLocalPush writes,
		// so this test exercises the real stand-in rather than the
		// recordingPush stub, to actually drive the on-disk state the
		// second reconcile is supposed to notice.
		Push: NewLocalPush(outputDir, "127.0.0.1:9090"),
	}

	if _, err := r.Reconcile(context.Background(), testRequest()); err != nil {
		t.Fatalf("Reconcile (1st): %v", err)
	}
	name := renderedName(t)
	jsonPath := filepath.Join(outputDir, name+".json")
	firstWrite, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read sidecar after 1st reconcile: %v", err)
	}

	if _, err := r.Reconcile(context.Background(), testRequest()); err != nil {
		t.Fatalf("Reconcile (2nd): %v", err)
	}
	secondWrite, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read sidecar after 2nd reconcile: %v", err)
	}
	if string(firstWrite) != string(secondWrite) {
		t.Errorf("sidecar changed on an unchanged-input reconcile:\n1st: %s\n2nd: %s", firstWrite, secondWrite)
	}
}

func TestReconcile_DetectsDigestChange(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace},
		Data:       map[string]string{TargetImageDigestKey: testDigest1},
	}
	cl := fake.NewClientBuilder().WithObjects(cm).Build()
	push := &recordingPush{}
	r := &Reconciler{
		Client:             cl,
		ConfigMapName:      testName,
		ConfigMapNamespace: testNamespace,
		OutputDir:          t.TempDir(),
		Push:               push.fn(),
	}

	if _, err := r.Reconcile(context.Background(), testRequest()); err != nil {
		t.Fatalf("Reconcile (digest1): %v", err)
	}
	if len(push.calls) != 1 {
		t.Fatalf("push called %d times after 1st reconcile, want 1", len(push.calls))
	}

	// Same name/namespace, only the digest changes -- render.Desired.Name
	// deliberately excludes the digest from its hash (an image-only
	// update must not force a DDI rebuild), so this is the one input
	// change that can slip past a naive "does <name>.json already exist"
	// check. Confirms it doesn't: push must fire again with the new
	// digest, not be swallowed as "already up to date".
	cm.Data[TargetImageDigestKey] = testDigest2
	if err := cl.Update(context.Background(), cm); err != nil {
		t.Fatalf("update configmap: %v", err)
	}

	if _, err := r.Reconcile(context.Background(), testRequest()); err != nil {
		t.Fatalf("Reconcile (digest2): %v", err)
	}
	if len(push.calls) != 2 {
		t.Fatalf("push called %d times after digest change, want 2", len(push.calls))
	}
	if push.calls[0].meta.Name != push.calls[1].meta.Name {
		t.Errorf("name changed across a digest-only update: %q vs %q (should be stable)",
			push.calls[0].meta.Name, push.calls[1].meta.Name)
	}
	if push.calls[1].meta.TargetImageDigest != testDigest2 {
		t.Errorf("2nd push TargetImageDigest = %q, want %q", push.calls[1].meta.TargetImageDigest, testDigest2)
	}
}

// TestReconcile_ReusesExistingBlobWithoutRebuilding pre-seeds outputDir
// with a <name>.raw whose bytes this test controls (standing in for a
// prior successful ddi.Build), then reconciles. Reconcile must not
// attempt to rebuild the DDI (this host lacks the tooling to anyway --
// see isBuildToolMissing) but must still recompute blobSha256 from the
// pre-existing blob, proving the name-keyed reuse path, not the
// build-attempt path, produced it.
func TestReconcile_ReusesExistingBlobWithoutRebuilding(t *testing.T) {
	outputDir := t.TempDir()
	name := renderedName(t)

	blob := []byte("pretend this is a confext DDI")
	if err := os.WriteFile(filepath.Join(outputDir, name+".raw"), blob, 0o644); err != nil {
		t.Fatalf("seed raw blob: %v", err)
	}
	wantSum := sha256.Sum256(blob)
	wantSha256 := hex.EncodeToString(wantSum[:])

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace},
		Data:       map[string]string{TargetImageDigestKey: testDigest1},
	}
	push := &recordingPush{}
	r := &Reconciler{
		Client:             fake.NewClientBuilder().WithObjects(cm).Build(),
		ConfigMapName:      testName,
		ConfigMapNamespace: testNamespace,
		OutputDir:          outputDir,
		Push:               push.fn(),
	}

	if _, err := r.Reconcile(context.Background(), testRequest()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(push.calls) != 1 {
		t.Fatalf("push called %d times, want 1", len(push.calls))
	}
	if got := push.calls[0].meta.BlobSha256; got != wantSha256 {
		t.Errorf("BlobSha256 = %q, want %q (sha256 of the pre-seeded blob, i.e. it was reused, not rebuilt or skipped)", got, wantSha256)
	}
}

func TestNewLocalPush_WritesProtojsonSidecar(t *testing.T) {
	outputDir := t.TempDir()
	push := NewLocalPush(outputDir, "127.0.0.1:9090")

	meta := &desiredpb.DesiredMetadata{
		Name:              "abc123",
		TargetImageDigest: testDigest1,
		BlobSha256:        buildSkippedSentinel,
	}
	rawPath := filepath.Join(outputDir, "abc123.raw") // deliberately absent
	if err := push(context.Background(), meta, rawPath); err != nil {
		t.Fatalf("push: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outputDir, "abc123.json"))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var got desiredpb.DesiredMetadata
	if err := protojson.Unmarshal(data, &got); err != nil {
		t.Fatalf("protojson.Unmarshal: %v", err)
	}
	if got.Name != meta.Name || got.TargetImageDigest != meta.TargetImageDigest || got.BlobSha256 != meta.BlobSha256 {
		t.Errorf("roundtripped sidecar = %+v, want %+v", &got, meta)
	}
}

func TestIsBuildToolMissing(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"systemd-repart not found", ddi.ErrSystemdRepartNotFound, true},
		{"mkfs.erofs message", errors.New(`ddi: systemd-repart ...: exit status 1: mkfs.erofs binary not available.`), true},
		{"unrelated failure", errors.New("permission denied"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isBuildToolMissing(c.err); got != c.want {
				t.Errorf("isBuildToolMissing(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
