# The Sidekick image: a static binary on distroless, run as user 65532 with
# no shell. `make image` builds it for linux/amd64 and linux/arm64; the
# release workflow builds, signs and pushes it.
#
# The ui stage builds the web UI, which the binary embeds, and the build stage
# compiles the binary. Both run on the build machine's own platform (the UI
# is the same for every architecture and Go cross compiles), so a
# multi-platform build needs no emulation.

# node:24.18.0-trixie-slim. The version matches web/.nvmrc.
FROM --platform=$BUILDPLATFORM docker.io/library/node:24.18.0-trixie-slim@sha256:ae91dcc111a68c9d2d81ff2a17bda61be126426176fde6fe7d08ab13b7f50573 AS ui

WORKDIR /src/web

COPY web/package.json web/package-lock.json web/.npmrc ./
RUN --mount=type=cache,target=/root/.npm npm ci

# The build reads the design tokens and the favicons from assets/ and writes
# to ../internal/web/dist.
COPY web/ ./
COPY assets/ /src/assets/
RUN npm run build

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
COPY --from=ui /src/internal/web/dist ./internal/web/dist

# Without the UI, the binary would serve a page that says so. Fail the build
# instead.
RUN test -f internal/web/dist/index.html

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
