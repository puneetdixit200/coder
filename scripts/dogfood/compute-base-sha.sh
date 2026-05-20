#!/usr/bin/env bash
# Deterministic 12-char content hash of base-image inputs for a distro.
# Used as a cache key for the ghcr.io/coder/oss-dogfood-base tag so
# commits that don't touch the base inputs reuse the previous build.
#
# This is NOT a strict content address: the base Dockerfile still
# pulls dynamic resources at build time (gh/buildx releases/latest,
# chrome stable_current_amd64.deb, apt mirror state, sh.rustup.rs).
# Two runs with identical checked-in files can still produce slightly
# different bytes. That's acceptable here because the dynamic drift
# is small and the cache-hit savings (no full base rebuild for a
# typo-fix commit, doc change, mise.toml bump, etc.) is large.
set -euo pipefail

distro="${1:?usage: $0 <22.04|26.04>}"

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

paths=(
	"dogfood/coder/ubuntu-${distro}/Dockerfile.base"
	"dogfood/coder/ubuntu-${distro}/files"
)
if [ "$distro" = "22.04" ]; then
	paths+=("dogfood/coder/ubuntu-${distro}/configure-chrome-flags.sh")
fi

# Skip editor turds; .swp / ~-files / dotfiles are noise for a build hash.
find "${paths[@]}" -type f \
	! -name '.*' \
	! -name '*.swp' \
	! -name '*~' \
	-print0 |
	LC_ALL=C sort -z |
	xargs -0 sha256sum |
	sha256sum |
	cut -c1-12
