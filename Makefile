.PHONY: ui build test run image cli

VERSION ?= dev

ui:
	cd web && npm ci && npm run build

build: ui
	CGO_ENABLED=0 go build -ldflags "-X main.version=$(VERSION)" -o bin/simplepanel ./cmd/simplepanel

test:
	go vet ./...
	go test ./...

# Run locally against your Docker daemon (needs `docker swarm init`).
run: build
	SP_DATA_DIR=./.data SP_TRAEFIK_DIR=./.data/traefik ./bin/simplepanel

image:
	docker build --build-arg VERSION=$(VERSION) -t simplepanel:$(VERSION) .

# The `sp` command-line client.
cli:
	CGO_ENABLED=0 go build -ldflags "-X main.version=$(VERSION)" -o bin/sp ./cmd/sp
