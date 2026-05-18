#!/usr/bin/env bash
# Local-only helper: runs `mise oci ...` inside a Linux container so
# macOS and Windows developers don't need a local Linux VM or a host
# install of mise. CI runs `mise oci` directly on its Linux runner; it
# does not use this script.
#
# Uses jdxcode/mise:latest rather than a version pin because the mise
# project does not publish a Docker tag for every release (the binary
# version baked into Dockerfile.base, v2026.4.19, has no corresponding
# Docker image). Since `mise oci` only constructs OCI layers and never
# affects the mise binary already in the base image, a wrapper-host
# version drift is harmless.
#
# Honors CONTAINER_RUNTIME=docker (default) or CONTAINER_RUNTIME=container
# (Apple's `container` CLI on macOS). Sets --platform linux/amd64 for
# `container` because its default platform is arm64 on Apple Silicon.
set -euo pipefail

MISE_IMAGE="jdxcode/mise:latest"
RUNTIME="${CONTAINER_RUNTIME:-docker}"
platform_arg=()
if [ "$RUNTIME" = "container" ]; then
	platform_arg=(--platform linux/amd64)
fi

token_arg=()
if [ -n "${GITHUB_TOKEN:-}" ]; then
	token_arg=(-e "GITHUB_TOKEN=$GITHUB_TOKEN")
fi

# Mount ~/.docker when present so skopeo/crane can find registry creds.
# Apple `container` CLI users without Docker Desktop won't have it;
# local builds don't push, so the skip is fine.
docker_config_arg=()
if [ -d "$HOME/.docker" ]; then
	docker_config_arg=(-v "$HOME/.docker:/root/.docker:ro")
fi

exec "$RUNTIME" run --rm "${platform_arg[@]}" \
	-v "$PWD":/src -w /src \
	"${docker_config_arg[@]}" \
	-e MISE_EXPERIMENTAL=1 \
	-e MISE_TRUSTED_CONFIG_PATHS=/src \
	"${token_arg[@]}" \
	"$MISE_IMAGE" \
	oci "$@"
