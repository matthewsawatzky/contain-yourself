# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.24.6-alpine3.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
ARG TARGETOS
ARG TARGETARCH
COPY cmd/controller ./cmd/controller
COPY cmd/workstationctl ./cmd/workstationctl
COPY cmd/vpnkeyctl ./cmd/vpnkeyctl
COPY internal/appstore ./internal/appstore
COPY internal/auth ./internal/auth
COPY internal/config ./internal/config
COPY internal/egress ./internal/egress
COPY internal/database ./internal/database
COPY internal/httpapi ./internal/httpapi
COPY internal/manifests ./internal/manifests
COPY internal/proxy ./internal/proxy
COPY internal/sharing ./internal/sharing
COPY internal/templates ./internal/templates
COPY internal/theme ./internal/theme
COPY internal/vpnprofiles ./internal/vpnprofiles
COPY internal/workerclient ./internal/workerclient
COPY internal/workstations ./internal/workstations
COPY migrations ./migrations
COPY pkg ./pkg
COPY web ./web
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
      go build -trimpath -ldflags="-s -w" -o /out/controller ./cmd/controller \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
      go build -trimpath -ldflags="-s -w" -o /out/workstationctl ./cmd/workstationctl \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
      go build -trimpath -ldflags="-s -w" -o /out/vpnkeyctl ./cmd/vpnkeyctl

FROM alpine:3.22.1
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S -g 10001 workstation \
 && adduser -S -D -H -u 10001 -G workstation workstation \
 && mkdir -p /data/backups \
 && chown -R workstation:workstation /data
COPY --from=build /out/controller /usr/local/bin/controller
COPY --from=build /out/workstationctl /usr/local/bin/workstationctl
COPY --from=build /out/vpnkeyctl /usr/local/bin/vpnkeyctl
USER workstation
EXPOSE 7080
ENTRYPOINT ["/usr/local/bin/controller"]
