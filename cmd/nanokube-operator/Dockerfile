# Minimal container build for nanokube-operator (Step 1 skeleton -- see
# internal/operator and
# docs/nanokube/2026-07-06-step1-implementation-plan-rev5.md, 実装項目5).
#
# Deliberately simpler than test/devenv/image/Containerfile (a full
# bootc node image): this is just a static Go binary in a small runtime
# base, run as an ordinary Deployment on Kind. It does NOT install
# systemd-repart/mkfs.erofs -- on a real node those live on the host,
# not in the operator's own container, and omitting them here exercises
# the same "DDI build tooling unavailable" path internal/operator's
# Reconcile already handles gracefully (see isBuildToolMissing).

FROM docker.io/library/golang:1.25.8 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/nanokube-operator ./cmd/nanokube-operator

FROM registry.fedoraproject.org/fedora-minimal:latest
COPY --from=build /out/nanokube-operator /usr/bin/nanokube-operator
ENTRYPOINT ["/usr/bin/nanokube-operator"]
