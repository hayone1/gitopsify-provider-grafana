VERSION ?= $(shell git describe --tags --match 'v*' --always --dirty 2>/dev/null || echo dev)

.PHONY: build install test lint record upstream live

build:
	CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=$(VERSION)" -o bin/gitopsify-provider-grafana .

# gitopsify finds providers on PATH: this is how to try a change.
GOBIN_DIR = $(or $(shell go env GOBIN),$(shell go env GOPATH)/bin)
install:
	CGO_ENABLED=0 go build -ldflags "-X main.version=$(VERSION)" -o "$(GOBIN_DIR)/gitopsify-provider-grafana" .

test:
	go test ./...

lint:
	go vet ./...
	gofmt -l . | (! grep .)

# Re-record testdata/api from a Grafana filled by test/live/seed.sh (see
# testdata/README.md), with GRAFANA_URL and GRAFANA_AUTH set. testdata/api
# changes only when everything could be read.
record:
	go run ./tools/record

# Refresh testdata/upstream (the Terraform schema and the Crossplane CRDs)
# from the versions pinned in internal/grafana/kinds.go. Needs terraform
# (TF=tofu for OpenTofu); runs crane through Go when it isn't installed.
upstream:
	go run ./tools/upstream

# Live test on a throwaway Hetzner machine (needs ../gitopsify and its .env).
live:
	cd ../gitopsify && UPLOAD=../gitopsify-provider-grafana RUN=/gitopsify-provider-grafana/test/live/crossplane.sh ./test/live/hetzner/e2e-on-vm.sh
