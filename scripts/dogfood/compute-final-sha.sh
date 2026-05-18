#!/usr/bin/env bash
# Deterministic 12-char content hash of (base inputs + mise inputs) for
# a distro. Used as the primary tag for the dogfood image produced by
# `mise oci build`, so re-running CI on an unchanged commit produces the
# same tag and the registry recognises the no-op.
set -euo pipefail

distro="${1:?usage: $0 <22.04|26.04>}"

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

base_sha="$("$repo_root/scripts/dogfood/compute-base-sha.sh" "$distro")"
mise_hash="$(sha256sum mise.toml mise.lock | sha256sum | cut -c1-12)"

printf '%s\n' "$base_sha-$mise_hash" | sha256sum | cut -c1-12
