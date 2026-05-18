# Use the branch name to differentiate test builds from actual pulled images,
# replacing forward slashes with hyphens, as forward slashes are not valid in
# tag names.
build_tag ?= $(shell git rev-parse --abbrev-ref HEAD | sed "s/\\//-/")

# The base Dockerfile consumes the repo root as build context so it can
# reach the project mise.toml. Each variant still tracks its own
# files/ tree under dogfood/coder/ubuntu-<release>/.
REPO_ROOT := $(shell git rev-parse --show-toplevel)

# Pick a container runtime. On macOS we prefer Apple's `container` CLI
# when present (it produces a Linux VM-backed amd64 image without
# Docker Desktop); otherwise fall back to docker. Linux always uses
# docker.
OS := $(shell uname -s)
ifeq ($(OS),Darwin)
  CONTAINER_RUNTIME ?= $(shell command -v container >/dev/null 2>&1 && echo container || echo docker)
else
  CONTAINER_RUNTIME ?= docker
endif

# Apple's `container` defaults to the host arch; the dogfood image is
# amd64-only, so pin it.
ifeq ($(CONTAINER_RUNTIME),container)
  PLATFORM_ARG := --platform linux/amd64
else
  PLATFORM_ARG :=
endif

# On Linux, run mise oci directly. Off Linux, route through the
# pinned-Linux-container wrapper.
ifeq ($(OS),Linux)
  MISE_OCI := MISE_EXPERIMENTAL=1 mise oci
else
  MISE_OCI := CONTAINER_RUNTIME=$(CONTAINER_RUNTIME) $(REPO_ROOT)/scripts/dogfood/mise-oci-wrapper.sh
endif

.PHONY: build build-ubuntu-22.04 build-ubuntu-26.04 \
	build-base-ubuntu-22.04 build-base-ubuntu-26.04 \
	update-keys update-keys-ubuntu-22.04 update-keys-ubuntu-26.04

build: build-ubuntu-22.04 build-ubuntu-26.04

build-base-ubuntu-22.04:
	$(CONTAINER_RUNTIME) build $(PLATFORM_ARG) \
		-f "$(REPO_ROOT)/dogfood/coder/ubuntu-22.04/Dockerfile.base" \
		-t "coderdev/oss-dogfood-base:22.04-local" \
		"$(REPO_ROOT)"

build-base-ubuntu-26.04:
	$(CONTAINER_RUNTIME) build $(PLATFORM_ARG) \
		-f "$(REPO_ROOT)/dogfood/coder/ubuntu-26.04/Dockerfile.base" \
		-t "coderdev/oss-dogfood-base:26.04-local" \
		"$(REPO_ROOT)"

build-ubuntu-22.04: build-base-ubuntu-22.04
	$(MISE_OCI) build \
		--from "coderdev/oss-dogfood-base:22.04-local" \
		--tag "codercom/oss-dogfood:22.04-$(build_tag)"

build-ubuntu-26.04: build-base-ubuntu-26.04
	$(MISE_OCI) build \
		--from "coderdev/oss-dogfood-base:26.04-local" \
		--tag "codercom/oss-dogfood:26.04-$(build_tag)"

update-keys: update-keys-ubuntu-22.04 update-keys-ubuntu-26.04

update-keys-ubuntu-22.04:
	./ubuntu-22.04/update-keys.sh

update-keys-ubuntu-26.04:
	./ubuntu-26.04/update-keys.sh
