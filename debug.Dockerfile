FROM golang:1.24.4

RUN go install github.com/go-delve/delve/cmd/dlv@v1.24

WORKDIR /
EXPOSE 40000