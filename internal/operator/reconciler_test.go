package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// renderedName computes the same content-hash name the reconciler itself
// derives for its default (no criSocket override) render input, so tests
// can pre-populate outputDir at the exact path Reconcile will look at.
// outputDir must be the same Reconciler.OutputDir a real reconcile
// against this input used (or will use): the name depends on the PKI
// and kubeconfig material render.ControlPlaneDesired generates into
// outputDir/credentials, which is why this can't be computed from cfg
// alone. render.Credentials's EnsureAll is idempotent, so calling this
// before or after the real Reconcile against the same outputDir yields
// the same name either way.
func renderedName(t *testing.T, outputDir string) string {
	t.Helper()
	return renderedNameForCRISocket(t, outputDir, "")
}

// renderedNameForCRISocket is renderedName generalized to an explicit
// cfg.NodeRegistration.CRISocket override, mirroring exactly what
// Reconcile itself does with CRISocketKey: an empty criSocket leaves the
// kubeadm-defaulted cfg untouched. Lets tests compute the expected name
// for a given ConfigMap criSocket value independently of the reconciler.
func renderedNameForCRISocket(t *testing.T, outputDir, criSocket string) string {
	t.Helper()
	cfg, err := kubeadmconfig.DefaultedStaticInitConfiguration()
	if err != nil {
		t.Fatalf("DefaultedStaticInitConfiguration: %v", err)
	}
	if criSocket != "" {
		cfg.NodeRegistration.CRISocket = criSocket
	}
	d, err := render.ControlPlaneDesired(cfg, filepath.Join(outputDir, "credentials"))
	if err != nil {
		t.Fatalf("render.ControlPlaneDesired: %v", err)
	}
	return d.Name()
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

func TestReconcile_RendersAndPushes(t *testing.T) {
	push := &recordingPush{}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace},
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
	}
	outputDir := t.TempDir()
	r := &Reconciler{
		Client:             fake.NewClientBuilder().WithObjects(cm).Build(),
		ConfigMapName:      testName,
		ConfigMapNamespace: testNamespace,
		OutputDir:          outputDir,
		// Reconcile itself writes the sidecar the idempotency check
		// reads (NewLocalPush no longer does, see
		// TestNewLocalPush_DoesNotWriteSidecar), so this test exercises
		// the real stand-in -- which never touches outputDir -- to prove
		// the on-disk state a second reconcile relies on comes from
		// Reconcile, not from whichever PushFunc happens to be wired in.
		Push: NewLocalPush(outputDir, "127.0.0.1:9090"),
	}

	if _, err := r.Reconcile(context.Background(), testRequest()); err != nil {
		t.Fatalf("Reconcile (1st): %v", err)
	}
	name := renderedName(t, outputDir)
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

// TestReconcile_CRISocketKeyChangesRenderedName confirms that a
// criSocket override actually changes the rendered kubelet config bytes
// (it flows into the kubelet instance config patch render.KubeletConfig
// writes), so it must change Desired.Name() too: the pushed name must
// match an independently-computed renderedNameForCRISocket, and differ
// from the no-override default name.
func TestReconcile_CRISocketKeyChangesRenderedName(t *testing.T) {
	const criSocket = "unix:///var/run/crio/crio.sock"
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace},
		Data:       map[string]string{CRISocketKey: criSocket},
	}
	push := &recordingPush{}
	outputDir := t.TempDir()
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

	want := renderedNameForCRISocket(t, outputDir, criSocket)
	if got := push.calls[0].meta.Name; got != want {
		t.Errorf("meta.Name = %q, want %q (rendered with criSocket override)", got, want)
	}
	if got := push.calls[0].meta.Name; got == renderedName(t, outputDir) {
		t.Errorf("meta.Name = %q, same as the no-override default -- criSocket change did not affect rendered content", got)
	}
}

// TestReconcile_EmptyCRISocketFallsBackToDefault checks the other half of
// the same contract: an absent (or explicitly empty) criSocket key must
// not error, and must render identically to a ConfigMap that never had
// the key at all -- i.e. it falls back to kubeadm's own default rather
// than e.g. setting CRISocket to the empty string.
func TestReconcile_EmptyCRISocketFallsBackToDefault(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace},
		Data:       map[string]string{CRISocketKey: ""},
	}
	push := &recordingPush{}
	outputDir := t.TempDir()
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
	if got, want := push.calls[0].meta.Name, renderedName(t, outputDir); got != want {
		t.Errorf("meta.Name = %q, want %q (empty criSocket must fall back to the default render)", got, want)
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
	name := renderedName(t, outputDir)

	blob := []byte("pretend this is a confext DDI")
	if err := os.WriteFile(filepath.Join(outputDir, name+".raw"), blob, 0o644); err != nil {
		t.Fatalf("seed raw blob: %v", err)
	}
	wantSum := sha256.Sum256(blob)
	wantSha256 := hex.EncodeToString(wantSum[:])

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace},
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

// TestNewLocalPush_DoesNotWriteSidecar replaces the former
// TestNewLocalPush_WritesProtojsonSidecar: NewLocalPush used to
// protojson-marshal meta to <name>.json as an incidental side effect of
// standing in for a real push, which is exactly what made
// readExistingMetadata's up-to-date check silently depend on which
// PushFunc happened to be wired in (NewGRPCPush never wrote it, so under
// --push-mode=grpc the check never fired). Now that Reconcile owns
// writing that sidecar itself, NewLocalPush must NOT also write it --
// this test locks in that contract so the duplicate side effect can't
// creep back in.
func TestNewLocalPush_DoesNotWriteSidecar(t *testing.T) {
	outputDir := t.TempDir()
	push := NewLocalPush(outputDir, "127.0.0.1:9090")

	meta := &desiredpb.DesiredMetadata{
		Name:       "abc123",
		BlobSha256: buildSkippedSentinel,
	}
	rawPath := filepath.Join(outputDir, "abc123.raw") // deliberately absent
	if err := push(context.Background(), meta, rawPath); err != nil {
		t.Fatalf("push: %v", err)
	}

	if _, err := os.Stat(filepath.Join(outputDir, "abc123.json")); !os.IsNotExist(err) {
		t.Fatalf("stat sidecar after NewLocalPush: err = %v, want a not-exist error (NewLocalPush must not write it)", err)
	}
}

// TestReconcile_IdempotentAcrossPushModes is the regression guard for the
// actual bug this task fixes: recordingPush, like the real NewGRPCPush
// (production's default PushFunc) and unlike the old NewLocalPush, never
// writes anything to outputDir on its own. Before Reconcile wrote the
// sidecar itself, readExistingMetadata never found one under this
// PushFunc, so the up-to-date check could never fire and every reconcile
// -- including a true no-op repeat -- re-built and re-pushed. Asserting
// the call count stays at 1 across two reconciles of the same input
// proves that gap is closed regardless of which PushFunc is wired in.
func TestReconcile_IdempotentAcrossPushModes(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace},
	}
	push := &recordingPush{}
	r := &Reconciler{
		Client:             fake.NewClientBuilder().WithObjects(cm).Build(),
		ConfigMapName:      testName,
		ConfigMapNamespace: testNamespace,
		OutputDir:          t.TempDir(),
		Push:               push.fn(),
	}

	if _, err := r.Reconcile(context.Background(), testRequest()); err != nil {
		t.Fatalf("Reconcile (1st): %v", err)
	}
	if len(push.calls) != 1 {
		t.Fatalf("push called %d times after 1st reconcile, want 1", len(push.calls))
	}

	if _, err := r.Reconcile(context.Background(), testRequest()); err != nil {
		t.Fatalf("Reconcile (2nd, unchanged input): %v", err)
	}
	if len(push.calls) != 1 {
		t.Errorf("push called %d times after 2nd reconcile with unchanged input, want still 1 (a true no-op repeat must be skipped, not re-pushed)", len(push.calls))
	}
}

// failingPush is a PushFunc stub that always fails and records how many
// times it was called, standing in for a push that never reaches the
// agent (e.g. it is unreachable) or that the agent rejects (e.g. a
// checksum mismatch).
type failingPush struct {
	calls int
}

func (p *failingPush) fn() PushFunc {
	return func(context.Context, *desiredpb.DesiredMetadata, string) error {
		p.calls++
		return errors.New("simulated push failure")
	}
}

// TestReconcile_DoesNotWriteSidecarOnPushFailure is the failure-ordering
// safety property Reconcile must uphold: writing the idempotency sidecar
// before Push has actually succeeded would make a future reconcile
// wrongly believe a failed push already completed, and then skip
// retrying it forever -- a worse bug than the one this task fixes. It
// reconciles once with a Push that always fails (asserting no sidecar
// lands on disk afterwards), then swaps in a Push that succeeds and
// reconciles again with the *same* input, asserting that second
// reconcile still actually calls Push rather than being incorrectly
// skipped as "already up to date".
func TestReconcile_DoesNotWriteSidecarOnPushFailure(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace},
	}
	outputDir := t.TempDir()
	failing := &failingPush{}
	r := &Reconciler{
		Client:             fake.NewClientBuilder().WithObjects(cm).Build(),
		ConfigMapName:      testName,
		ConfigMapNamespace: testNamespace,
		OutputDir:          outputDir,
		Push:               failing.fn(),
	}

	if _, err := r.Reconcile(context.Background(), testRequest()); err == nil {
		t.Fatal("Reconcile: want an error from a failing push, got nil")
	}
	if failing.calls != 1 {
		t.Fatalf("failing push called %d times, want 1", failing.calls)
	}
	name := renderedName(t, outputDir)
	jsonPath := filepath.Join(outputDir, name+".json")
	if _, err := os.Stat(jsonPath); !os.IsNotExist(err) {
		t.Fatalf("stat sidecar after a failed push: err = %v, want a not-exist error -- a failed push must never be recorded as done", err)
	}

	// A subsequent reconcile of the SAME input (e.g. controller-runtime's
	// retry, or a later resync before anything changed) must actually
	// retry the push, not be wrongly skipped as a no-op because of a
	// sidecar the failed attempt should never have written.
	retrying := &recordingPush{}
	r.Push = retrying.fn()
	if _, err := r.Reconcile(context.Background(), testRequest()); err != nil {
		t.Fatalf("Reconcile (retry after failure): %v", err)
	}
	if len(retrying.calls) != 1 {
		t.Fatalf("push called %d times on retry, want 1 (must not have been skipped as a no-op)", len(retrying.calls))
	}
}

// TestReconcile_PropagatesFileContextsPathToDDIBuild proves
// Reconciler.FileContextsPath actually reaches ddi.BuildInput, without
// depending on this host having systemd-repart/mkfs.erofs installed
// (CI and most dev hosts don't -- see isBuildToolMissing). A
// FileContextsPath containing whitespace makes ddi.Build fail its own
// input validation before it ever shells out to systemd-repart, so the
// resulting error is deterministic regardless of host tooling. If
// FileContextsPath were dropped on the way from Reconciler to
// ddi.BuildInput, Build would instead take the tool-missing path (on a
// bare host, the sentinel case already covered by
// TestReconcile_RendersAndPushes) or succeed (on a host with the DDI
// toolchain) -- neither of which is this error.
func TestReconcile_PropagatesFileContextsPathToDDIBuild(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace},
	}
	push := &recordingPush{}
	r := &Reconciler{
		Client:             fake.NewClientBuilder().WithObjects(cm).Build(),
		ConfigMapName:      testName,
		ConfigMapNamespace: testNamespace,
		OutputDir:          t.TempDir(),
		FileContextsPath:   "/path with a space/file_contexts",
		Push:               push.fn(),
	}

	_, err := r.Reconcile(context.Background(), testRequest())
	if err == nil {
		t.Fatal("Reconcile: want an error (invalid FileContextsPath reaching ddi.Build), got nil")
	}
	if !strings.Contains(err.Error(), "FileContextsPath must not contain whitespace") {
		t.Errorf("Reconcile error = %q, want it to surface ddi.Build's FileContextsPath validation (proves the field was wired through to ddi.BuildInput)", err)
	}
	if len(push.calls) != 0 {
		t.Errorf("push called %d times, want 0 (a failed build must never be pushed)", len(push.calls))
	}
}

// TestReconcile_RejectsOversizedRenderAtRenderTime proves an oversized
// render is rejected at render time (IMPLEMENTATION_PLAN.md §2.2: the
// 512KiB bootstrap-Secret cap is enforced by the renderer itself, not
// by whatever consumes the desired document downstream) rather than
// Reconcile happily building and pushing an oversized Desired.
// CRISocketKey is the one render input this ConfigMap-based Reconciler
// exposes to a caller, and it flows into the rendered kubelet config
// (see TestReconcile_CRISocketKeyChangesRenderedName), so an oversized
// CRISocket value is the lever used here to drive the render past
// render.MaxRenderedBytes.
func TestReconcile_RejectsOversizedRenderAtRenderTime(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace},
		Data:       map[string]string{CRISocketKey: "unix://" + strings.Repeat("a", 600<<10)},
	}
	push := &recordingPush{}
	r := &Reconciler{
		Client:             fake.NewClientBuilder().WithObjects(cm).Build(),
		ConfigMapName:      testName,
		ConfigMapNamespace: testNamespace,
		OutputDir:          t.TempDir(),
		Push:               push.fn(),
	}

	_, err := r.Reconcile(context.Background(), testRequest())
	if err == nil {
		t.Fatal("Reconcile: want an error from an oversized render, got nil")
	}
	if !strings.Contains(err.Error(), "rendered set is") {
		t.Errorf("Reconcile error = %q, want render.MaxRenderedBytes rejection to surface", err)
	}
	if len(push.calls) != 0 {
		t.Errorf("push called %d times, want 0 (an oversized render must never be pushed)", len(push.calls))
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
