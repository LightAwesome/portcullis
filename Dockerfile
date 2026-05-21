# syntax=docker/dockerfile:1.7
#
# Multi-stage build for Portcullis.
#
# Stage 1 (builder) compiles a fully static Linux/amd64 binary using
# Go 1.22 on Alpine. Stage 2 (final) is a FROM scratch image containing
# only the binary, CA certificates for upstream TLS, and the migrations
# directory needed for golang-migrate to run at boot.
#
# Build:   docker build -t portcullis:dev .
# Inspect: docker run --rm portcullis:dev --help
# Size:    docker images portcullis  (target: under 30 MB)

# ============================================================
# Stage 1 — Builder
# ============================================================

FROM golang:1.26-alpine AS builder

# Alpine doesn't ship git by default; go mod needs it for some module
# fetches (the ones that come from non-mirror sources).
RUN apk add --no-cache git

WORKDIR /src

# Copy module files first, separately from source. This lets Docker
# cache the `go mod download` layer — it only re-runs when go.mod or
# go.sum changes, not on every source edit. Speeds up incremental
# builds from minutes to seconds.
COPY go.mod go.sum ./
RUN go mod download

# Now copy the full source. Anything in .dockerignore is excluded.
COPY . .

# Build the binary.
#
# CGO_ENABLED=0      → pure-Go binary, no libc dependency
# GOOS=linux         → target Linux regardless of host OS (macOS, Windows)
# GOARCH=amd64       → target x86_64 (Hetzner CX22 is amd64)
# -trimpath          → strip absolute paths for reproducible builds
# -ldflags="-s -w"   → strip symbol table + DWARF debug info (~30% smaller)
# -tags="netgo,osusergo"
#                    → force pure-Go network and user implementations
#                      (redundant with CGO_ENABLED=0 but explicit)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
    -trimpath \
    -ldflags="-s -w" \
    -tags="netgo,osusergo" \
    -o /out/portcullis \
    ./cmd/portcullis

# ============================================================
# Stage 2 — Final image
# ============================================================

FROM scratch

# Copy CA certificates so the gateway can verify upstream HTTPS
# certificates (api.openai.com, etc.). Without this, every outbound
# TLS connection fails with "x509: certificate signed by unknown
# authority".
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# Copy migrations so they can be applied at deploy time. We don't
# automatically run migrations on container start — that's a deploy-time
# choice, not a runtime one — but having them inside the image means
# the same artifact carries its schema.
COPY --from=builder /src/migrations /migrations

# Copy the binary.
COPY --from=builder /out/portcullis /portcullis

# Document the listening port. EXPOSE is documentation only — it does
# not publish the port. The Compose file or `docker run -p` does that.
EXPOSE 8080

# Run as the unprivileged numeric UID 65534 ("nobody" on most systems,
# though /etc/passwd is absent in scratch so the name doesn't resolve).
# A compromised process can't escalate to root because it never had
# root in the first place.
USER 65534:65534

# ENTRYPOINT not CMD so flags pass through cleanly. Run as:
#   docker run portcullis            -> portcullis raise (default arg below)
#   docker run portcullis muster     -> portcullis muster (overrides default)
ENTRYPOINT ["/portcullis"]
CMD ["raise"]
