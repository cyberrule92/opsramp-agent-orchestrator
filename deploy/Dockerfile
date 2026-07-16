# syntax=docker/dockerfile:1

# Multi-target build: select the binary with --build-arg CMD=orchestrator|demo-agent.
FROM golang:1.25-bookworm AS build
WORKDIR /src

# Cache dependencies first.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# Build.
COPY . .
ARG CMD=orchestrator
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${CMD}

# Minimal runtime image (static binary, no shell).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]
