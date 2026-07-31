.PHONY: build images test fmt vet compose-config store store-check up down logs backup release

build:
	go build ./cmd/...

images:
	./scripts/dev-build.sh

test:
	go test -race ./...

fmt:
	gofmt -w cmd internal pkg web

vet:
	go vet ./...

compose-config:
	docker compose config --quiet

store:
	go run ./cmd/storectl build

store-check:
	go run ./cmd/storectl check

up:
	./scripts/dev-up.sh

down:
	docker compose down

logs:
	docker compose logs -f controller docker-worker

backup:
	docker compose exec controller workstationctl backup

release:
	@test -n "$(VERSION)" || (echo "usage: make release VERSION=v1.0.0"; exit 1)
	./scripts/release.sh "$(VERSION)" dist
