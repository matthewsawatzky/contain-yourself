FROM golang:1.24.6-alpine3.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/controller ./cmd/controller \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/workstationctl ./cmd/workstationctl

FROM alpine:3.22.1
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S -g 10001 workstation \
 && adduser -S -D -H -u 10001 -G workstation workstation \
 && mkdir -p /data/backups \
 && chown -R workstation:workstation /data
COPY --from=build /out/controller /usr/local/bin/controller
COPY --from=build /out/workstationctl /usr/local/bin/workstationctl
USER workstation
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/controller"]
