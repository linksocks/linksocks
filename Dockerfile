# Build stage
FROM golang:1.21-alpine AS builder

WORKDIR /build

# Install necessary build tools
RUN apk add --no-cache git

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
ENV GOFLAGS=-buildvcs=false
RUN CGO_ENABLED=0 go build -o linksocks ./cmd/linksocks

# Final stage
FROM alpine:3.19

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/linksocks /app/

# Create non-root user
RUN adduser -D -H -h /app linksocks && \
    chown -R linksocks:linksocks /app

USER linksocks

ENTRYPOINT ["/app/linksocks"] 