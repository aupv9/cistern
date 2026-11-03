VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build build-ui ui dev-ui test race lint image e2e e2e-image e2e-k8s clean
build:
	go build -ldflags "$(LDFLAGS)" -o bin/cistern ./cmd/cistern
ui:
	cd web && npm ci && npm run build
build-ui:
	go build -tags ui -ldflags "$(LDFLAGS)" -o bin/cistern ./cmd/cistern
dev-ui:
	./scripts/dev-ui.sh
test:
	go test ./...
race:
	go test -race ./...
lint:
	test -z "$$(find . -name '*.go' -not -path './web/node_modules/*' -not -path './.dev-ui/*' -exec gofmt -l {} +)"
	go vet ./...
image:
	docker build --build-arg VERSION=$(VERSION) -t cistern:dev .
e2e:
	./scripts/e2e-local.sh
e2e-image: image
	./scripts/e2e-local.sh image
e2e-k8s: image
	./scripts/e2e-k8s.sh
clean:
	rm -rf bin
