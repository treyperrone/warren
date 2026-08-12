#!/usr/bin/env bash
# Build AWS's session-manager-plugin from source and install the results into
# internal/plugin/assets/, where the per-platform //go:embed directives pick them up.
#
# Why build rather than download AWS's binaries:
#
# The plugin's source is Apache-2.0 — the LICENSE file and a header on every source file say so —
# which permits redistribution with the licence and NOTICE included. The prebuilt binaries AWS
# serves from S3 are a different artefact, and the repo's THIRD-PARTY file calls the plugin "AWS
# Content ... licensed to you under [the AWS Customer Agreement]". Homebrew reaches the same
# conclusion in practice: it ships session-manager-plugin as a cask that downloads from AWS rather
# than rehosting the binary. warren has to embed it to work in airgapped environments, so it
# builds the Apache-2.0 source instead of redistributing AWS's build. That also makes the result
# reproducible and lets the exact provenance be stated.
#
# A TAG, never a branch. The mainline VERSION file reads 1.3.0.0 while the version AWS actually
# distributes is 1.2.835.0 — mainline is ahead of what has been released, so building it would
# ship code AWS has not shipped. Every release is tagged, so the tag is the source of truth.
#
# The plugin predates modules: there is no go.mod, and vendor/ is a second GOPATH root rather than
# a vendor directory (see its makefile: GOPATH := $(GOTEMPPATH):$(GO_SPACE)/vendor). Hence
# GO111MODULE=off and the two-root GOPATH below. GOPATH mode is deprecated and some future Go will
# drop it; this script runs only when AWS cuts a plugin release, so there is plenty of warning.
set -euo pipefail

VERSION="${1:-}"
if [ -z "$VERSION" ]; then
  echo "usage: $0 <plugin-version-tag>   e.g. $0 1.2.835.0" >&2
  exit 2
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ASSETS="$REPO_ROOT/internal/plugin/assets"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

SRC="$WORK/src/github.com/aws/session-manager-plugin"
mkdir -p "$(dirname "$SRC")"

echo "==> cloning aws/session-manager-plugin at tag $VERSION"
git clone --depth 1 --branch "$VERSION" -q \
  https://github.com/aws/session-manager-plugin.git "$SRC"

# AWS does not keep its in-tree version constant aligned with its release tags: at tag 1.2.835.0
# both VERSION and src/version/version.go read 1.3.0.0, so the binary reports 1.3.0.0 while AWS's
# own distributed 1.2.835.0 build reports 1.2.835.0.
#
# The source is deliberately NOT patched to fix that. version.Version is a const, so -X cannot
# override it, and editing the file would make this a modified work — Apache-2.0 §4(b) then
# requires prominent notices stating what was changed, which is real obligation for no real gain.
# The tag is what identifies the provenance, so the tag is what gets recorded. The discrepancy is
# reported here so it reads as AWS's inconsistency rather than as a build gone wrong.
reported="$(sed -n 's/.*Version = "\(.*\)".*/\1/p' "$SRC/src/version/version.go" | head -1)"
echo "==> tag $VERSION; source reports itself as ${reported:-unknown}"

export GO111MODULE=off
export CGO_ENABLED=0
export GOFLAGS=
export GOPATH="$WORK:$SRC/vendor"

mkdir -p "$ASSETS"

# GOOS GOARCH -> asset filename. Matches the plugin_<goos>_<goarch>.go embed directives.
build() {
  local goos="$1" goarch="$2" ext="${3:-}"
  local out="$ASSETS/session-manager-plugin-${goos}-${goarch}${ext}"
  echo "==> building ${goos}/${goarch}"
  ( cd "$SRC" && GOOS="$goos" GOARCH="$goarch" \
      go build -trimpath -ldflags "-s -w" -o "$out" ./src/sessionmanagerplugin-main )
  chmod 0644 "$out" # an embedded asset, not something to run in place
}

build linux   amd64
build linux   arm64
build darwin  amd64
build darwin  arm64
build windows amd64 .exe

printf '%s\n' "$VERSION" > "$REPO_ROOT/internal/plugin/version.txt"

echo
echo "==> built from tag $VERSION"
ls -l "$ASSETS"
echo
echo "recorded internal/plugin/version.txt = $VERSION"
