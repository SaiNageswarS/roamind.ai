# syntax=docker/dockerfile:1.7
#
# Build context: repository root.
#   docker build -f gateway/Dockerfile -t <user>/roamind-gateway:v1 .
#
# Gateway is a single static Go binary. We disable the Go workspace
# (GOWORK=off) and rely on the `replace` directive in gateway/go.mod
# pointing at ../proto so the build is self-contained.

############################
# Stage 1 — build
############################
FROM golang:1.26-alpine AS builder

ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOWORK=off

# ca-certificates is needed at *runtime* (the distroless final image),
# but we install it here so we can copy /etc/ssl/certs into the
# scratch-like runtime layer below. Required for outbound TLS to
# MongoDB Atlas, Telegram, etc.
RUN apk add --no-cache ca-certificates

WORKDIR /src

# Copy only what gateway needs (proto module + gateway module).
# Generated protobuf .pb.go files are committed under proto/generated/
# so protoc is NOT required at image build time.
COPY proto/  ./proto/
COPY gateway/ ./gateway/

WORKDIR /src/gateway

# Warm the module cache, then build a stripped, trimmed static binary.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go build \
        -ldflags="-s -w" \
        -trimpath \
        -o /out/roamind-gateway .

############################
# Stage 2 — runtime
############################
FROM gcr.io/distroless/static-debian12:nonroot

# Trust roots for outbound TLS (MongoDB Atlas, Telegram, etc.).
# distroless/static already bundles these, but copying explicitly
# from the builder guarantees an up-to-date Mozilla bundle and is
# tolerant to base-image variants.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

COPY --from=builder /out/roamind-gateway /usr/local/bin/roamind-gateway

# gRPC for CLI clients; HTTP for health/metrics (intra-app only on ACA).
EXPOSE 50051 8081

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/roamind-gateway"]
