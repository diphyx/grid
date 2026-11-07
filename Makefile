# Both images come from the Dockerfile, one target each; `make run ARGS="node run"` drives
# the grid binary locally, `make run ARGS="test node --addr …"` the test kit.
IMAGE ?= ghcr.io/diphyx/grid
PLATFORMS ?= linux/amd64,linux/arm64

.PHONY: init
init:
	go mod tidy
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

.PHONY: format
format:
	go fmt ./...

.PHONY: lint
lint:
	@test -x ./custom-gcl || golangci-lint custom
	./custom-gcl run --fix=false ./...

# Rebuilds ./custom-gcl after a version bump in .custom-gcl.yml.
.PHONY: lint-build
lint-build:
	golangci-lint custom

.PHONY: test
test:
	go test -race ./...

.PHONY: run
run:
	go run . $(ARGS)

# The docs as GitHub Pages serves them, at http://localhost:4000: Docsify renders the Markdown
# in the browser, so any static server will do.
.PHONY: docs
docs:
	python3 -m http.server 4000 --directory docs

.PHONY: build
build:
	docker buildx build --platform $(PLATFORMS) --target node --tag $(IMAGE)-node .
	docker buildx build --platform $(PLATFORMS) --target hub --tag $(IMAGE)-hub .

.PHONY: push
push:
	docker buildx build --platform $(PLATFORMS) --target node --tag $(IMAGE)-node --push .
	docker buildx build --platform $(PLATFORMS) --target hub --tag $(IMAGE)-hub --push .

# One platform, loaded into the local docker, for trying the images here.
.PHONY: build-local
build-local:
	docker buildx build --target node --tag $(IMAGE)-node --load .
	docker buildx build --target hub --tag $(IMAGE)-hub --load .

# Tags main and pushes the tag; release.yml does the rest: tests, the binaries,
# `go install github.com/diphyx/grid@$(VERSION)`, then the images.
.PHONY: release
release:
	@test -n "$(VERSION)" || (echo 'usage: make release VERSION=v1.2.3' && exit 1)
	@test "$$(git branch --show-current)" = main || (echo 'release from main' && exit 1)
	@git fetch -q origin main && test "$$(git rev-parse HEAD)" = "$$(git rev-parse origin/main)" \
		|| (echo 'main differs from origin/main: pull or push first' && exit 1)
	git tag -a $(VERSION) -m $(VERSION)
	git push origin $(VERSION)
