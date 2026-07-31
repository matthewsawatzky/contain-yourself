# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.24.6-alpine3.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
ARG TARGETOS
ARG TARGETARCH
COPY cmd/wslan ./cmd/wslan
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
      go build -trimpath -ldflags="-s -w" -o /out/wslan ./cmd/wslan

FROM alpine:3.22.1
RUN apk add --no-cache ca-certificates dnsmasq iproute2 iptables wireguard-tools \
 && mkdir -p /run/wslan
COPY --from=build /out/wslan /usr/local/bin/wslan
COPY build/wslan-entrypoint.sh /usr/local/bin/wslan-entrypoint
RUN chmod 755 /usr/local/bin/wslan-entrypoint
EXPOSE 9000
ENTRYPOINT ["/usr/local/bin/wslan-entrypoint"]
