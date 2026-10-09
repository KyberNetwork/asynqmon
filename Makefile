.PHONY: api assets build docker

NODE_PATH ?= $(PWD)/ui/node_modules
# Node 17+ uses OpenSSL 3, which rejects the legacy md4 hashing that
# react-scripts 5 / webpack rely on (error:0308010C). Re-enable it on those
# versions so `make build` works on modern Node without manual env tweaks.
NODE_MAJOR := $(shell node -p "process.versions.node.split('.')[0]" 2>/dev/null)
NODE_OPENSSL_LEGACY := $(shell [ "$(NODE_MAJOR)" -ge 17 ] 2>/dev/null && echo --openssl-legacy-provider)
assets:
	@if [ ! -d "$(NODE_PATH)"  ]; then cd ./ui && yarn install --modules-folder $(NODE_PATH); fi
	cd ./ui && NODE_OPTIONS="$(NODE_OPENSSL_LEGACY)" yarn build --modules-folder $(NODE_PATH)

# This target skips the overhead of building UI assets.
# Intended to be used during development.
api:
	go build -o api ./cmd/asynqmon

# Build a release binary.
build: assets
	go build -o asynqmon ./cmd/asynqmon

IMAGE ?= asia.gcr.io/kyber-operation/base/asynqmon
TAG ?= v0.0.3

# Build the image and push it to the registry.
# Override the tag to avoid overwriting an existing one: make docker TAG=v0.0.2
docker:
	podman build --platform=linux/amd64 -t $(IMAGE):$(TAG) .
