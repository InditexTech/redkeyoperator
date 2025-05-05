FROM golang:1.23

RUN go install github.com/go-delve/delve/cmd/dlv@v1.23.1

WORKDIR /
EXPOSE 40000
