FROM golang:1.24.6-alpine3.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker

FROM alpine:3.22.1
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/worker /usr/local/bin/worker
EXPOSE 8090
ENTRYPOINT ["/usr/local/bin/worker"]
