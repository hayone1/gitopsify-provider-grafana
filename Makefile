VERSION ?= $(shell git describe --tags --match 'v*' --always --dirty 2>/dev/null || echo dev)

.PHONY: build install test lint record upstream live

build:
	CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=$(VERSION)" -o bin/gitopsify-provider-grafana .

# gitopsify finds providers on PATH: this is how to try a change.
install:
	CGO_ENABLED=0 go install -ldflags "-X main.version=$(VERSION)" .

test:
	go test ./...

lint:
	go vet ./...
	gofmt -l . | (! grep .)

# Re-record testdata/api from a Grafana filled by test/live/seed.sh (see
# testdata/README.md).
record: build
	rm -rf testdata/api
	echo '{"settings":{"url":"$(or $(GRAFANA_URL),http://localhost:3300)","auth":"$(or $(GRAFANA_AUTH),admin:admin)"}}' | \
	  GITOPSIFY_GRAFANA_RECORD=testdata/api ./bin/gitopsify-provider-grafana check >/dev/null
	echo '{"settings":{"url":"$(or $(GRAFANA_URL),http://localhost:3300)","auth":"$(or $(GRAFANA_AUTH),admin:admin)"}}' | \
	  GITOPSIFY_GRAFANA_RECORD=testdata/api ./bin/gitopsify-provider-grafana discover >/dev/null

# Live test on a throwaway Hetzner machine (needs ../gitopsify and its .env).
live:
	cd ../gitopsify && UPLOAD=../gitopsify-provider-grafana RUN=/gitopsify-provider-grafana/test/live/crossplane.sh ./test/live/hetzner/e2e-on-vm.sh
