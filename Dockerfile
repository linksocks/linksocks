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
RUN go build -o wssocks ./cmd

# Final stage
FROM alpine:3.19

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/wssocks /app/

# Create non-root user
RUN adduser -D -H -h /app wssocks && \
    chown -R wssocks:wssocks /app

USER wssocks

ENTRYPOINT ["/app/wssocks"] 