# Minimal container build for nanokube-operator (Step 1 skeleton -- see
# internal/operator and
# docs/nanokube/2026-07-06-step1-implementation-plan-rev5.md, 実装項目5).
#
# Deliberately simpler than test/devenv/image/Containerfile (a full
# bootc node image): this is just a static Go binary in a small runtime
# base, run as an ordinary Deployment on Kind. It DOES install
# systemd-repart/mkfs.erofs (実装項目6, real-machine verify): rev5's
# design judgment 3 places DDI building server-side, hosted by the
# operator from Step 1 onward, so the operator's own container is where
# a real (non-sentinel) ddi.Build needs to run. Package set matches
# .github/workflows/ci.yaml's "ddi" job exactly (systemd-container +
# systemd-udev + erofs-utils), the recipe already proven there to make
# systemd-repart --make-ddi=confext work for real.

FROM docker.io/library/golang:1.25.8 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/nanokube-operator ./cmd/nanokube-operator

FROM registry.fedoraproject.org/fedora-minimal:latest
RUN dnf install -y systemd-container systemd-udev erofs-utils --setopt=install_weak_deps=False -q && dnf clean all
COPY --from=build /out/nanokube-operator /usr/bin/nanokube-operator
ENTRYPOINT ["/usr/bin/nanokube-operator"]
