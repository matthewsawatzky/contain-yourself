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
	diff -u apps/browser/app.yaml app_store/apps/browser/app.yaml
	diff -u apps/browser/icon.svg app_store/apps/browser/icon.svg
	diff -u apps/code/app.yaml app_store/apps/code/app.yaml
	diff -u apps/code/icon.svg app_store/apps/code/icon.svg
	diff -u apps/files/app.yaml app_store/apps/files/app.yaml
	diff -u apps/files/icon.svg app_store/apps/files/icon.svg

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
