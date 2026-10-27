# The Sidekick image: a static binary on distroless, run as user 65532 with
# no shell. `make image` builds it for linux/amd64 and linux/arm64; the
# release workflow builds, signs and pushes it.
#
# The build stage runs on the build machine's own platform and Go cross
# compiles, so a multi-platform build needs no emulation.

# golang:1.26.10-trixie
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.26.10-trixie@sha256:909771fef53e3dcb0950255f0c9a8b4728c3b841e61509c7b29b098de7ee02d4 AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
	CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
	go build -trimpath \
	-ldflags "-s -w -X github.com/nourtalaat/sidekick/internal/buildinfo.Version=$VERSION -X github.com/nourtalaat/sidekick/internal/buildinfo.Commit=$COMMIT" \
	-o /out/sidekick ./cmd/sidekick

# gcr.io/distroless/static-debian13:nonroot
FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99

ARG VERSION=dev
ARG COMMIT=unknown

LABEL org.opencontainers.image.title="Sidekick" \
	org.opencontainers.image.description="Investigates alerts and reports whether they are real" \
	org.opencontainers.image.source="https://github.com/nourtalaat/sidekick" \
	org.opencontainers.image.licenses="Apache-2.0" \
	org.opencontainers.image.version="$VERSION" \
	org.opencontainers.image.revision="$COMMIT"

COPY --from=build /out/sidekick /usr/local/bin/sidekick
COPY LICENSE NOTICE /usr/share/doc/sidekick/

USER 65532:65532

# server.listen and server.metricsListen.
EXPOSE 8080 9090

ENTRYPOINT ["/usr/local/bin/sidekick"]
CMD ["serve"]
