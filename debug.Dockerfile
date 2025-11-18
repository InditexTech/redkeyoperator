# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

# Define the desired Golang version
ARG GOLANG_VERSION=1.24.6

# Use an official Golang image with a specific version based on Debian
FROM golang:${GOLANG_VERSION}-trixie

# Define the desired Delve version
ARG DELVE_VERSION=1.25

# Install Delve debugger
RUN go install "github.com/go-delve/delve/cmd/dlv@v${DELVE_VERSION}"

# Install redis-cli by adding the redis package (required to debug) and other useful tools
RUN apt update -y && apt install -y redis-tools curl procps

WORKDIR /
EXPOSE 40000
