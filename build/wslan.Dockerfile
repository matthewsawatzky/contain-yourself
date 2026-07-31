FROM golang:1.24.6-alpine3.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/wslan ./cmd/wslan

FROM alpine:3.22.1
RUN apk add --no-cache ca-certificates dnsmasq iproute2 iptables wireguard-tools \
 && mkdir -p /run/wslan
COPY --from=build /out/wslan /usr/local/bin/wslan
COPY build/wslan-entrypoint.sh /usr/local/bin/wslan-entrypoint
RUN chmod 755 /usr/local/bin/wslan-entrypoint
EXPOSE 9000
ENTRYPOINT ["/usr/local/bin/wslan-entrypoint"]
