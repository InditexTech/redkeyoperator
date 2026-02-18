# SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
#
# SPDX-License-Identifier: Apache-2.0

FROM golang:1.25-alpine AS builder

# install git and basic build tools so xk6 can fetch & build extensions
RUN apk add --no-cache git build-base ca-certificates

RUN go install go.k6.io/xk6/cmd/xk6@latest
RUN xk6 build \
    --with github.com/grafana/xk6-redis \
    --output /k6

FROM alpine:3.23
COPY --from=builder /k6 /usr/bin/k6
COPY k6scripts/ /scripts/
ENTRYPOINT ["/usr/bin/k6"]
