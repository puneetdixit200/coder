#!/usr/bin/env bash
# Deterministic 12-char content hash of base-image inputs for a distro.
# Walks the relevant on-disk files so local iteration without a commit
# still yields a stable tag.
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
