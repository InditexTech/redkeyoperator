# Use an official Golang image with a specific version
ARG GO_VERSION=1.24.0
FROM golang:${GO_VERSION} AS builder

# Set environment variables
ENV GOPROXY=https://proxy.golang.org,direct
ENV CGO_ENABLED=0

# Create a working directory for the application
WORKDIR /app

# Copy Go module files and download dependencies first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy the entire source code
COPY / .

# Build the Go application with version information
RUN go build -ldflags "-X main.version=2.0.0" -o goApp

# Use a smaller base image for the final stage
FROM alpine:3.15 AS final

# Install redis-cli by adding the redis package
RUN apk add --no-cache redis

# Create a non-root user
RUN addgroup -S goApp && adduser -S -u 10000 -G goApp goApp

# Copy the Go binary from the builder stage
COPY --from=builder /app/goApp /goApp

# Switch to the non-root user
USER goApp

# Set the entry point
ENTRYPOINT ["/goApp"]
