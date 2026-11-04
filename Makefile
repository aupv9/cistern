VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
DIST_TARGETS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build build-ui ui dev-ui test race lint image e2e e2e-image e2e-k8s e2e-oidc e2e-ui dist release-notes clean
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
e2e-oidc:
	./scripts/e2e-local.sh oidc
e2e-ui:
	./scripts/e2e-local.sh ui
dist:
	test -f web/dist/client/_shell.html || { echo "web/dist/client is missing: run make ui first" >&2; exit 1; }
	rm -rf dist && mkdir -p dist
	set -e; for t in $(DIST_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; name=cistern_$(VERSION)_$${os}_$${arch}; \
		mkdir -p dist/$$name; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -tags timetzdata,ui -ldflags "-s -w $(LDFLAGS)" -o dist/$$name/cistern ./cmd/cistern; \
		cp LICENSE NOTICE README.md CHANGELOG.md dist/$$name/; \
		tar -C dist -czf dist/$$name.tar.gz $$name; \
		rm -rf dist/$$name; \
	done
	cd dist && shasum -a 256 *.tar.gz > SHA256SUMS && cat SHA256SUMS
release-notes:
	@mkdir -p dist
	@v=$(VERSION); v=$${v#v}; awk -v want="## [$$v]" 'index($$0, "## [") == 1 { if (on) exit; on = index($$0, want) == 1; next } on && /^\[[^]]*\]: http/ { exit } on' CHANGELOG.md \
		| sed -e '/./,$$!d' | sed -e ':a' -e '/^\n*$$/{$$d;N;ba' -e '}' > dist/release-notes.md
	@test -s dist/release-notes.md || { echo "CHANGELOG.md has no section ## [$${VERSION#v}]" >&2; rm -f dist/release-notes.md; exit 1; }
	@cat dist/release-notes.md
clean:
	rm -rf bin dist
