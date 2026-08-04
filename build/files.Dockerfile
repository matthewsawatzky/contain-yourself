# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.24.6-alpine3.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
ARG TARGETOS
ARG TARGETARCH
COPY cmd/files ./cmd/files
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
      go build -trimpath -ldflags="-s -w" -o /out/files ./cmd/files

FROM alpine:3.22.1
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S -g 10001 workspace \
 && adduser -S -D -H -u 10001 -G workspace workspace
COPY --from=build /out/files /usr/local/bin/files
# The workspace volume is mounted here and chowned to this uid by the worker's
# storage initialisation, so the server never needs to run as root.
USER workspace
EXPOSE 7080
ENTRYPOINT ["/usr/local/bin/files"]
