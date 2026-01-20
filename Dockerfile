# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

### Build stage

# Define the desired Golang version
ARG GOLANG_VERSION=1.24.6

# Use an official Golang image with a specific version based on Debian
FROM golang:${GOLANG_VERSION}-trixie AS builder

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/ cmd/
COPY api/ api/
COPY v1client/ v1client/
COPY controllers/ controllers/
COPY internal/ internal/

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -o manager ./cmd/


### Final stage

# Use a smaller base image for the final stage
FROM debian:trixie-slim AS final

# Install some useful tools
RUN apt update && apt upgrade -y && apt install -y curl procps

LABEL org.opencontainers.image.source="https://github.com/inditextech/redkeyoperator"

# Copy the built binary from the builder stage, use a non-root user and set entrypoint
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532
ENTRYPOINT ["/manager"]
