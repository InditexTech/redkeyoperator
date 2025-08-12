# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

# Build the manager binary
FROM golang:1.24.5 AS builder

WORKDIR /workspace

# Copy the Go Modules manifests
COPY webhook/go.mod webhook/go.mod
COPY webhook/go.sum webhook/go.sum

COPY go.mod go.mod

WORKDIR /workspace/webhook

# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY webhook/main.go main.go
COPY ../ /workspace/


# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -o webhook .

# Use Red Hat Universal Base Image 9 Minimal to package the manager binary.
# Refer to https://www.redhat.com/en/blog/introducing-red-hat-universal-base-image for more details.
FROM redhat/ubi9-minimal:9.1.0

LABEL org.opencontainers.image.source="https://github.com/inditextech/redkeyoperator"

RUN microdnf update -y && microdnf install procps -y

WORKDIR /
COPY --from=builder /workspace/webhook .
USER 65532:65532
ENTRYPOINT ["/webhook"]
