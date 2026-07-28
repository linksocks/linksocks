# Build stage
# golang:alpine tracks the current stable Go release on Docker Hub.
# GOTOOLCHAIN=auto self-upgrades if go.mod requires a newer toolchain than the image.
FROM golang:alpine AS builder

WORKDIR /build

ENV GOTOOLCHAIN=auto
ENV GOFLAGS=-buildvcs=false

# Install necessary build tools
RUN apk add --no-cache git ca-certificates

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 go build -o linksocks ./cmd/linksocks

# Final stage: floating alpine tag; no manual OS pin to bump each release.
FROM alpine:latest

WORKDIR /app

RUN apk add --no-cache ca-certificates && \
    adduser -D -H -h /app linksocks

COPY --from=builder /build/linksocks /app/

RUN chown -R linksocks:linksocks /app

USER linksocks

# Default environment for Docker deployments
ENV LINKSOCKS_RETRY_AUTH=true

ENTRYPOINT ["/app/linksocks"]
