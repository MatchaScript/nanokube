# -*- mode: Python -*-
#
# Dev loop for nanokube-operator: `tilt up` builds the Go binary on the
# host, rebuilds a minimal runtime image around it, deploys
# cmd/nanokube-operator/deploy.yaml to the current kind cluster, and
# live-updates the binary in place (sync + restart) on source changes.
#
# This is a standalone Tiltfile, NOT the cluster-api Tiltfile +
# tilt-provider.json integration the Step 3 plan (docs/nanokube/plans/
# 2026-07-19-step3-plan.md, Task C) describes for CABPR. That route
# requires the provider to carry a kustomize config/default (CRDs, RBAC,
# manager, webhook -- the full kubebuilder scaffold cluster-api's
# tilt-prepare shells out to `kustomize build` against, see
# hack/tools/internal/tilt-prepare/main.go:361-362 in a cluster-api
# checkout) and a clusterctl metadata.yaml. nanokube-operator has neither
# yet -- it reconciles a plain ConfigMap (internal/operator/reconciler.go),
# not a CRD -- and CAPI provider-ization is explicitly out of scope until
# the roadmap re-editing decides it (see the plan's 未裁定 section). This
# Tiltfile covers only what Task C's verify line asks for: `tilt up`
# deploys the operator to kind with hot reload.
#
# Requires three env vars on hosts where kind/tilt run through rootless
# podman instead of a docker daemon:
#   KIND_EXPERIMENTAL_PROVIDER=podman
#   DOCKER_HOST=unix:///run/user/<uid>/podman/podman.sock
#   DOCKER_BUILDKIT=0
# The last one works around podman's docker-compat API not speaking the
# buildkit gRPC protocol Tilt's docker_build otherwise negotiates first
# (fails with "failed to dial gRPC: unable to upgrade to h2c, received
# 404" without it). Export all three before `tilt up`/`tilt ci`.

allow_k8s_contexts(os.getenv('NANOKUBE_TILT_CONTEXT', 'kind-nanokube-tilt'))

load('ext://restart_process', 'docker_build_with_restart')

local_resource(
    'nanokube-operator-binary',
    cmd = 'CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o .tiltbuild/bin/nanokube-operator ./cmd/nanokube-operator',
    deps = ['cmd/nanokube-operator', 'internal/operator', 'internal/render', 'internal/ddi', 'contract/desiredpb', 'go.mod', 'go.sum'],
    labels = ['nanokube-operator'],
)

docker_build_with_restart(
    'localhost/nanokube-operator',
    '.',
    dockerfile_contents = '\n'.join([
        'FROM registry.fedoraproject.org/fedora-minimal:latest',
        # tar is required in-container for Tilt live_update's sync step
        # (it execs tar to copy the rebuilt binary in); fedora-minimal
        # does not include it by default.
        'RUN dnf install -y systemd-container systemd-udev erofs-utils tar --setopt=install_weak_deps=False -q && dnf clean all',
        'COPY .tiltbuild/bin/nanokube-operator /usr/bin/nanokube-operator',
        'ENTRYPOINT ["/usr/bin/nanokube-operator"]',
    ]),
    entrypoint = ['/usr/bin/nanokube-operator'],
    only = ['.tiltbuild/bin/nanokube-operator'],
    live_update = [
        sync('.tiltbuild/bin/nanokube-operator', '/usr/bin/nanokube-operator'),
    ],
)

k8s_yaml('cmd/nanokube-operator/deploy.yaml')
k8s_resource('nanokube-operator', resource_deps = ['nanokube-operator-binary'], labels = ['nanokube-operator'])
