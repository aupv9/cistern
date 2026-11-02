VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build test race lint image e2e clean
build:
	go build -ldflags "$(LDFLAGS)" -o bin/cistern ./cmd/cistern
test:
	go test ./...
race:
	go test -race ./...
lint:
	test -z "$$(gofmt -l .)"
	go vet ./...
image:
	docker build --build-arg VERSION=$(VERSION) -t cistern:dev .
e2e:
	./scripts/e2e-local.sh
clean:
	rm -rf bin
