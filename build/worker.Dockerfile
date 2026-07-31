# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.24.6-alpine3.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
ARG TARGETOS
ARG TARGETARCH
COPY cmd/worker ./cmd/worker
COPY internal/config ./internal/config
COPY internal/dockerworker ./internal/dockerworker
COPY internal/vpnprofiles ./internal/vpnprofiles
COPY pkg ./pkg
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
      go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker

FROM alpine:3.22.1
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/worker /usr/local/bin/worker
EXPOSE 8090
ENTRYPOINT ["/usr/local/bin/worker"]
