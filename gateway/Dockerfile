# syntax=docker/dockerfile:1.7
#
# gateway/Dockerfile
# Distroless static Go binary for the ifix-ai-gateway service.
#
# Refs:
#   - .planning/phases/02-gateway-core-multi-tenant-auth/02-08-PLAN.md Task 1
#   - .planning/phases/02-gateway-core-multi-tenant-auth/02-PATTERNS.md §gateway/Dockerfile
#   - pod/health-bridge/Dockerfile (Phase 1 reference for the same 2-stage pattern)

# =========================================================================
# Stage 1: builder — Go toolchain + gateway source + sqlc-generated code
# =========================================================================
FROM golang:1.23-alpine AS builder
WORKDIR /build

RUN apk add --no-cache ca-certificates git tzdata

# Install sqlc so `go generate` (if we added one) could regenerate.
# For Phase 2 we commit the sqlc output, so this RUN is a safety net —
# if CI forgot to run `sqlc generate` before build, the resulting binary
# still compiles against a stale gen/ directory.
#
# Note: the CI job runs sqlc generate BEFORE `docker build`; this stage
# only verifies the generated code compiles.

# Dependency layer cache: copy go.mod/go.sum first so source changes
# don't bust the module-download cache.
COPY go.mod go.sum ./
RUN go mod download

# Copy shared pkg/openai and the entire gateway subtree.
COPY pkg/openai ./pkg/openai
COPY gateway ./gateway

# Build version metadata injected via -ldflags. CI sets GATEWAY_VERSION;
# local builds fall back to "dev".
ARG GATEWAY_VERSION=dev

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath \
    -ldflags="-s -w -X github.com/ifixtelecom/gpu-ifix/gateway/internal/obs.BuildVersion=${GATEWAY_VERSION}" \
    -o /gateway \
    ./gateway/cmd/gateway

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath \
    -ldflags="-s -w -X github.com/ifixtelecom/gpu-ifix/gateway/internal/obs.BuildVersion=${GATEWAY_VERSION}" \
    -o /gatewayctl \
    ./gateway/cmd/gatewayctl

# =========================================================================
# Stage 2: runtime — distroless static (no shell, no libc, ~2 MB base)
# =========================================================================
FROM gcr.io/distroless/static-debian12 AS runtime

# Timezone database so TZ=America/Sao_Paulo resolves at runtime.
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
ENV TZ=America/Sao_Paulo

COPY --from=builder /gateway /gateway
COPY --from=builder /gatewayctl /gatewayctl

# Gateway HTTP port (fixed per CONTEXT.md Plumbing: :8080 on VPS; Traefik
# / DNS added in Fase 10).
EXPOSE 8080

# SIGTERM is the default signal docker sends on `docker stop`; match it
# explicitly so graceful shutdown (25s budget) kicks in reliably.
STOPSIGNAL SIGTERM

# Default entrypoint is the gateway server. To run gatewayctl:
#   docker run --rm --entrypoint /gatewayctl <image> migrate up
ENTRYPOINT ["/gateway"]
