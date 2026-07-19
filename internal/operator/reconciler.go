// Package operator implements nanokube-operator's Step 1 skeleton
// reconcile loop: watch a single, fixed ConfigMap (the stand-in desired
// source before CRDs land in Step 4), render it (internal/render) and
// build a confext DDI from the result (internal/ddi), then hand the
// outcome to a PushFunc — the seam that dials --agent-addr and calls the
// generated desiredpb.AgentClient.PushDesired (NewGRPCPush, the
// production default) or, for local dev without a running agent, just
// logs what it would have pushed instead (NewLocalPush). Reconcile
// itself does not change between the two, and it -- not either
// PushFunc -- owns writing the <name>.json sidecar the idempotency check
// below relies on, so that check means the same thing regardless of
// which PushFunc is wired in. See
// docs/nanokube/2026-07-06-step1-implementation-plan-rev5.md, 実装項目5.
package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	kubeadmconfig "k8s.io/kubernetes/cmd/kubeadm/app/util/config"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/MatchaScript/nanokube/contract/desiredpb"
	"github.com/MatchaScript/nanokube/internal/ddi"
	"github.com/MatchaScript/nanokube/internal/render"
)

// CRISocketKey is the ConfigMap Data key the reconciler reads to override
// cfg.NodeRegistration.CRISocket before rendering the kubelet config —
// the Step 1 stand-in for a real kubelet config parameter input (実装項目
// 6.a). Absent or empty leaves cfg untouched, so it renders with
// kubeadm's own default (DefaultedStaticInitConfiguration already sets
// kubeadmconstants.DefaultCRISocket) exactly as before this key existed.
const CRISocketKey = "criSocket"

// buildSkippedSentinel is written as DesiredMetadata.BlobSha256 when
// ddi.Build could not run because systemd-repart (and, transitively,
// mkfs.erofs) is missing from PATH -- the case on a bare Kind/dev host.
// It is a deliberately non-hex-looking sentinel, never a real sha256,
// so nothing downstream can mistake a skipped build for a built one.
const buildSkippedSentinel = "build-skipped-systemd-repart-unavailable"

// isBuildToolMissing reports whether err reflects this host lacking the
// tooling ddi.Build needs, rather than a genuine build failure. There
// are two known shapes, both confirmed on this repo's own dev host:
// systemd-repart itself absent from PATH (ddi.ErrSystemdRepartNotFound,
// checked with errors.Is), or systemd-repart present but unable to find
// its own mkfs.erofs helper, which ddi.Build only surfaces as
// systemd-repart's captured stderr text ("mkfs.erofs binary not
// available.") wrapped into a plain error -- there is no sentinel error
// for that case in internal/ddi to match on structurally.
func isBuildToolMissing(err error) bool {
	return errors.Is(err, ddi.ErrSystemdRepartNotFound) || strings.Contains(err.Error(), "mkfs.erofs")
}

// readExistingMetadata returns the sidecar previously written to
// jsonPath, or ok=false if none exists yet or it can't be parsed -- a
// corrupt sidecar is treated the same as "none" (just rebuilt below)
// rather than becoming a standing reconcile failure.
func readExistingMetadata(jsonPath string) (m *desiredpb.DesiredMetadata, ok bool) {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil, false
	}
	m = &desiredpb.DesiredMetadata{}
	if err := protojson.Unmarshal(data, m); err != nil {
		return nil, false
	}
	return m, true
}

// PushFunc delivers a rendered-and-(maybe-)built desired document
// somewhere. meta is always populated (with a sentinel BlobSha256 when
// the build was skipped); rawPath is the confext DDI blob's local path
// and may not exist -- implementations must check before reading it.
// Implementations are not responsible for recording that the push
// happened -- Reconcile does that itself (see the <name>.json sidecar
// write at the end of Reconcile) -- so a PushFunc need only attempt the
// delivery and report success or failure.
//
// NewGRPCPush is the production implementation: it dials --agent-addr
// and calls desiredpb.AgentClient.PushDesired with meta and rawPath's
// bytes. NewLocalPush is the Step 1 stand-in kept around for local dev
// without a running agent: it just logs what it would have pushed and to
// which agent address, instead of making a network call.
// Reconciler.Reconcile does not need to change between the two.
type PushFunc func(ctx context.Context, meta *desiredpb.DesiredMetadata, rawPath string) error

// NewLocalPush returns the Step 1 stand-in PushFunc: instead of dialing
// anything, it just logs what it would have pushed and to which agent
// address. agentAddr is documentation-only here -- see the package doc
// comment. outputDir is likewise documentation-only: it names the
// directory this stand-in would have written to, which in practice is
// the same Reconciler.OutputDir that Reconcile itself already writes
// <name>.raw and (after a successful push) <name>.json into, regardless
// of which PushFunc is wired in.
func NewLocalPush(outputDir, agentAddr string) PushFunc {
	return func(ctx context.Context, meta *desiredpb.DesiredMetadata, rawPath string) error {
		log.FromContext(ctx).Info("would push to agent (Step 1 stand-in: no network call made)",
			"agentAddr", agentAddr, "outputDir", outputDir, "name", meta.Name, "rawPath", rawPath)
		return nil
	}
}

// Reconciler reconciles exactly one ConfigMap -- identified by
// ConfigMapName/ConfigMapNamespace -- into a rendered + built confext
// DDI, handed to Push. It ignores every other object the manager's
// watch delivers.
type Reconciler struct {
	Client             client.Client
	ConfigMapName      string
	ConfigMapNamespace string
	// OutputDir is where the built <name>.raw lands and where the
	// up-to-date check below looks for <name>.json. It is also the
	// parent of credsDir (see Reconcile), the operator's persistent PKI
	// store.
	OutputDir string
	// FileContextsPath, when non-empty, is passed through to
	// ddi.BuildInput.FileContextsPath so the built DDI carries build-time
	// SELinux labels (IMPLEMENTATION_PLAN.md §6). Empty means no
	// labeling. cmd/nanokube-operator wires this from the
	// NANOKUBE_FILE_CONTEXTS environment variable.
	FileContextsPath string
	Push             PushFunc
}

// SetupWithManager registers r against mgr, watching ConfigMaps only.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}).
		Named("nanokube-operator-configmap").
		Complete(r)
}

// +kubebuilder:rbac:groups=,resources=configmaps,verbs=get;list;watch

// Reconcile implements the one-cycle loop 実装項目5 of the implementation
// plan asks for: detect input change -> render -> attempt build -> push
// -> record that the push succeeded -> done. Every step after
// render/build depends on the previous one having succeeded; Reconcile
// returns (and controller-runtime retries) on the first unexpected
// error. In particular, the <name>.json sidecar readExistingMetadata
// checks is written by Reconcile itself, only after r.Push returns nil
// -- never before, and never by a PushFunc -- so the idempotency check
// means the same thing under every PushFunc, and a failed push is never
// mistaken for a completed one on the next reconcile.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if req.Name != r.ConfigMapName || req.Namespace != r.ConfigMapNamespace {
		// Not our ConfigMap; the manager's watch is cluster-wide but we
		// only care about one object. No CRD/admission-time filtering
		// exists yet (Step 4), so this check is the filter.
		return ctrl.Result{}, nil
	}

	var cm corev1.ConfigMap
	if err := r.Client.Get(ctx, req.NamespacedName, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("target ConfigMap not found, nothing to do", "name", req.Name, "namespace", req.Namespace)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("operator: get configmap: %w", err)
	}

	// Render is deterministic given (kubeadm's static defaults, this
	// process's environment, and whatever credsDir already holds) for
	// this skeleton -- there is no per-node variance yet -- so it is
	// safe to always run, which is also how the confext version name (a
	// content hash of the rendered files) is discovered in the first
	// place. Unlike the Step 1 kubelet-config-only render this replaces,
	// render.ControlPlaneDesired is not side-effect-free: it writes PKI
	// and kubeconfig material into credsDir (render.Credentials). That
	// write is idempotent -- EnsureAll preserves existing valid files --
	// so repeated renders of the same input still converge to the same
	// bytes and the same Name().
	cfg, err := kubeadmconfig.DefaultedStaticInitConfiguration()
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("operator: DefaultedStaticInitConfiguration: %w", err)
	}
	if criSocket := cm.Data[CRISocketKey]; criSocket != "" {
		cfg.NodeRegistration.CRISocket = criSocket
	}
	// credsDir is the operator's persistent PKI/kubeconfig store -- a
	// stand-in until Step 5 decides how CP secrets are actually held.
	// It must stay the same path across reconciles (and process
	// restarts) for the cluster's CA identity to stay stable; nesting it
	// under OutputDir is what makes that automatic here.
	credsDir := filepath.Join(r.OutputDir, "credentials")
	desired, err := render.ControlPlaneDesired(cfg, credsDir)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("operator: render.ControlPlaneDesired: %w", err)
	}
	name := desired.Name()
	logger.Info("rendered desired document", "name", name)

	rawPath := filepath.Join(r.OutputDir, name+".raw")
	jsonPath := filepath.Join(r.OutputDir, name+".json")

	// Push necessity is revision matching: if the sidecar already on
	// disk carries this same name, this is a true no-op repeat (see
	// ARCHITECTURE.md "agent の適用と状態報告" -- push の要否は報告
	// revision ≠ desired revision). A missing or unparseable sidecar just
	// means "not up to date" -- rebuilt below like any first run.
	if existing, ok := readExistingMetadata(jsonPath); ok && existing.GetName() == name {
		logger.Info("already up to date, skipping build and push", "name", name)
		return ctrl.Result{}, nil
	}

	if err := os.MkdirAll(r.OutputDir, 0o755); err != nil {
		return ctrl.Result{}, fmt.Errorf("operator: mkdir output dir: %w", err)
	}

	var blobSha256 string
	if raw, err := os.ReadFile(rawPath); err == nil {
		// The DDI blob for this name was already built by an earlier
		// reconcile (its content is a pure function of desired.Files,
		// which name already hashes over) -- rebuilding would only
		// reproduce the same bytes, so reuse it instead of re-invoking
		// systemd-repart.
		sum := sha256.Sum256(raw)
		blobSha256 = hex.EncodeToString(sum[:])
		logger.Info("DDI already built for this name, reusing it", "name", name, "rawPath", rawPath, "bytes", len(raw))
	} else {
		blobSha256 = buildSkippedSentinel
		buildErr := ddi.Build(ddi.BuildInput{
			Name:             name,
			Files:            desired.Files,
			FileContextsPath: r.FileContextsPath,
		}, rawPath)
		switch {
		case buildErr == nil:
			raw, err := os.ReadFile(rawPath)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("operator: read back built blob: %w", err)
			}
			sum := sha256.Sum256(raw)
			blobSha256 = hex.EncodeToString(sum[:])
			logger.Info("built confext DDI", "name", name, "rawPath", rawPath, "bytes", len(raw))
		case isBuildToolMissing(buildErr):
			// A real environment gap (bare Kind/dev hosts typically lack
			// systemd-repart and/or mkfs.erofs), not something to paper
			// over: log it clearly and keep going with a sentinel so the
			// rest of the reconcile loop (push + its logging) remains
			// exercisable end to end even where a real DDI can't be
			// produced here.
			logger.Info("WARNING: DDI build tooling unavailable, skipping build; writing sidecar with a sentinel blob_sha256 instead of a real one",
				"name", name, "blobSha256", blobSha256, "buildErr", buildErr)
		default:
			return ctrl.Result{}, fmt.Errorf("operator: ddi.Build: %w", buildErr)
		}
	}

	meta := &desiredpb.DesiredMetadata{
		Name:       name,
		BlobSha256: blobSha256,
	}
	if err := r.Push(ctx, meta, rawPath); err != nil {
		return ctrl.Result{}, fmt.Errorf("operator: push: %w", err)
	}

	// Only now that Push has actually succeeded is it safe to record it:
	// writing the sidecar any earlier (or unconditionally, regardless of
	// Push's outcome) would make a future reconcile believe a failed push
	// already succeeded and wrongly skip retrying it -- a correctness bug
	// worse than the idempotency gap this sidecar-write closes. Same
	// MarshalOptions contract/fixtures/gen uses, so the sidecar format
	// matches the golden fixtures.
	data, err := (protojson.MarshalOptions{Multiline: true, Indent: "  "}).Marshal(meta)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("operator: marshal metadata: %w", err)
	}
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		return ctrl.Result{}, fmt.Errorf("operator: write %s: %w", jsonPath, err)
	}

	return ctrl.Result{}, nil
}
