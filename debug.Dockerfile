FROM golang:1.24.6

RUN go install github.com/go-delve/delve/cmd/dlv@v1.25

WORKDIR /
EXPOSE 40000