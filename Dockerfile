# syntax=docker/dockerfile:1
# build: compile in the image (release); local: hack/e2e/up.sh prebuilt bin/.
ARG BIN=build
FROM --platform=$BUILDPLATFORM golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG TARGETOS TARGETARCH
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/csi-driver-rclone ./cmd/csi-driver-rclone

FROM scratch AS bin-local
COPY bin/csi-driver-rclone /out/csi-driver-rclone

FROM build AS bin-build
FROM bin-${BIN} AS bin

# Node plugin overrides to root + privileged in the chart; Mount Pods keep nonroot.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=bin /out/csi-driver-rclone /csi-driver-rclone
ARG VERSION=dev
LABEL org.opencontainers.image.source="https://github.com/BlackDark/csi-driver-rclone" \
      org.opencontainers.image.description="Kubernetes CSI driver mounting rclone remotes as volumes" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="Apache-2.0"
ENTRYPOINT ["/csi-driver-rclone"]
