# syntax=docker/dockerfile:1

# Pinned so rebuilding a release is reproducible. postgresql18-client must
# match the postgres:18 server in docker-compose.yaml.
ARG GO_VERSION=1.25
ARG ALPINE_VERSION=3.24

# build: cross-compiles on the build host's native arch, so producing the
# arm64 image never runs the Go toolchain under emulation.
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
      go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
      go build -trimpath -ldflags="-s -w" -o /out/configenv ./cmd/configenv

# dev: local hot reload only (docker-compose.override.yaml mounts the source
# at /src and runs air). Never pushed. air v1.67.1 is the last release that
# builds with Go 1.25.
FROM golang:${GO_VERSION}-alpine AS dev
RUN apk add --no-cache bash postgresql18-client \
 && go install github.com/air-verse/air@v1.67.1
WORKDIR /src

# runtime: the image pushed to GHCR. Must stay the LAST stage so a build
# without --target (make release) produces it.
FROM alpine:${ALPINE_VERSION} AS runtime
ARG VERSION=dev
LABEL org.opencontainers.image.source="https://github.com/akbarryyan/pg-aggregator-back" \
      org.opencontainers.image.title="whuzpay-backend" \
      org.opencontainers.image.version="${VERSION}"
RUN apk add --no-cache bash ca-certificates tzdata postgresql18-client \
 && addgroup -S -g 10001 app \
 && adduser -S -D -H -u 10001 -G app -h /app app
WORKDIR /app
COPY --from=build /out/api /out/configenv /app/bin/
COPY migrations ./migrations
COPY seeds ./seeds
COPY scripts ./scripts
ENV PATH="/app/bin:${PATH}"
USER app
EXPOSE 8080
# .config.toml is mounted at /app/.config.toml at runtime (never baked in);
# api's default -config path resolves to it from WORKDIR /app.
ENTRYPOINT ["/app/bin/api"]
