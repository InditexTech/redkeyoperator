# Build the manager binary
FROM golang:1.23.1 as builder

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
COPY v1alpha1client/ v1alpha1client/
COPY v1client/ v1client/
COPY controllers/ controllers/
COPY internal/ internal/

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -o manager ./cmd/

# Use Red Hat Universal Base Image 9 Minimal to package the manager binary.
# Refer to https://www.redhat.com/en/blog/introducing-red-hat-universal-base-image for more details.
FROM redhat/ubi9-minimal:9.1.0

LABEL org.opencontainers.image.source="https://github.com/inditextech/redisoperator"

RUN microdnf update -y && microdnf install procps -y

WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532
ENTRYPOINT ["/manager"]
