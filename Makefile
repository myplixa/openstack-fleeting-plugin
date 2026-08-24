.DEFAULT_GOAL := build

export NAME := fleeting-plugin-openstack
export OUT_PATH ?= out
export CGO_ENABLED ?= 0

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
REVISION := $(shell git rev-parse --short=8 HEAD 2>/dev/null || echo unknown)
BRANCH := $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)
BUILT := $(shell date -u +%Y-%m-%dT%H:%M:%S%z)
PKG := $(shell go list .)

GO_LDFLAGS := -X $(PKG).Version=$(VERSION) \
              -X $(PKG).Revision=$(REVISION) \
              -X $(PKG).Branch=$(BRANCH) \
              -X $(PKG).BuildUser=$(shell whoami) \
              -X $(PKG).BuildDate=$(BUILT) \
              -w -extldflags '-static'

.PHONY: build
build:
	@mkdir -p $(OUT_PATH)
	go build -ldflags "$(GO_LDFLAGS)" -o $(OUT_PATH)/$(NAME) ./cmd/$(NAME)/...

.PHONY: mods
mods:
	go mod download

.PHONY: test
test: mods
	go test -v -timeout=15m ./...

.PHONY: integration-test
integration-test: mods
	@mkdir -p $(OUT_PATH)
	go build -o $(OUT_PATH)/$(NAME)-realtest ./cmd/$(NAME)/...
	go test -v -timeout=15m ./test/integration/... \
		-plugin-binary-path=$(PWD)/$(OUT_PATH)/$(NAME)-realtest \
		-config-path=$(PWD)/test/integration/config.json \
		|| (rm -f $(OUT_PATH)/$(NAME)-realtest; exit 1)
	rm -f $(OUT_PATH)/$(NAME)-realtest

.PHONY: clean
clean:
	rm -fr $(OUT_PATH)
