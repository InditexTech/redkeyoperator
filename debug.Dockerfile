# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

FROM golang:1.24.6

RUN go install github.com/go-delve/delve/cmd/dlv@v1.25

# Install redis-cli by adding the redis package (required to debug Redkey Robin)
RUN apt update -y && apt install -y redis-tools curl procps

WORKDIR /
EXPOSE 40000