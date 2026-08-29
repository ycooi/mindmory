#!/bin/sh
# Assemble Mindmory MCP release tarballs.
#
# Run directly with the local Go toolchain. Artifacts land in packaging/dist/.
#
# The tarballs contain ONLY compiled binaries and user documentation —
# never runtime data, credentials, tests, or private material. Binaries are
# built with -trimpath and stripped symbols (-s -w). Optional garble builds
# are available but disabled by default.
#
# The product is the lite daemon, operator CLI, and MCP stdio bridge.
set -eu

VERSION="${VERSION:-$(cat VERSION)}"
DIST="packaging/dist"
PLATFORMS="darwin/amd64 darwin/arm64 linux/amd64 linux/arm64"
COMMANDS="mindmoryd-lite mindmoryctl mindmory-mcp-stdio"
OBFUSCATE="${RELEASE_OBFUSCATE:-0}"
# Keep optional garble builds bounded on smaller build hosts.
GARBLE_PARALLEL="${GARBLE_PARALLEL:-1}"
GARBLE_MEM_LIMIT="${GARBLE_MEM_LIMIT:-2200MiB}"
GARBLE_GOGC="${GARBLE_GOGC:-35}"

license_check=$(mktemp)
trap 'rm -f "$license_check"' EXIT
sh scripts/generate-third-party-licenses.sh "$license_check"
if ! cmp -s THIRD_PARTY_LICENSES.txt "$license_check"; then
  echo "THIRD_PARTY_LICENSES.txt is stale; regenerate it before releasing" >&2
  exit 1
fi

mkdir -p "$DIST"
rm -f "$DIST"/mindmory-mcp-*.tar.gz "$DIST/SHA256SUMS"

if [ "$OBFUSCATE" = "1" ]; then
  export PATH="$PATH:$(go env GOPATH)/bin"
  if ! command -v garble >/dev/null 2>&1; then
    echo "installing garble..."
    go install mvdan.cc/garble@latest
  fi
fi

# Fail the release if the staging directory contains anything outside the
# allowlist below, or if any staged file or binary embeds a private marker
# (set PRIVATE_MARKERS to a space-separated list of strings that must never
# ship — host paths, owner names, tokens, ...).
check_stage_clean() {
  stage="$1"
  bad=""
  for f in $(find "$stage" -type f | sort); do
    rel="${f#"$stage"/}"
    case "$rel" in
      bin/mindmoryd-lite|bin/mindmoryctl|bin/mindmory-mcp-stdio|mindmory-config.example.sh|AGENT_INSTALL.md|README.md|LICENSE|NOTICE.md|THIRD_PARTY_NOTICES.md|THIRD_PARTY_LICENSES.txt|VERSION|setup.sh|dsh/README.md|dsh/cordis.patch.example.yml|dsh/checkpoint-relay.mjs|integrations/README.md|integrations/checkpoint-hook.sh|integrations/codex/README.md|integrations/codex/config.toml.example|integrations/codex/hooks.json.example|integrations/claude-code/README.md|integrations/claude-code/mcp.json.example|integrations/claude-code/settings.json.example|integrations/generic/README.md) ;;
      *) echo "LEAK: unexpected file in package: $rel" >&2; bad=1 ;;
    esac
  done
  [ -z "$bad" ] || return 1
  markers="${PRIVATE_MARKERS:-}"
  if [ -n "$markers" ] && command -v strings >/dev/null 2>&1; then
    for m in $markers; do
      if strings "$stage"/bin/* 2>/dev/null | grep -qF "$m"; then
        echo "LEAK: private marker '$m' found in staged binaries" >&2
        return 1
      fi
    done
  fi
  echo "   leak check passed for $stage"
}

for platform in $PLATFORMS; do
  os="${platform%/*}"
  arch="${platform#*/}"
  stage="$DIST/mindmory-mcp-$os-$arch"
  rm -rf "$stage"
  mkdir -p "$stage/bin"
  echo "==> building $platform"
  for command in $COMMANDS; do
    if [ "$OBFUSCATE" = "1" ] && command -v garble >/dev/null 2>&1; then
      if GOMEMLIMIT="$GARBLE_MEM_LIMIT" GOGC="$GARBLE_GOGC" CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" garble -literals build -p "$GARBLE_PARALLEL" \
        -trimpath -ldflags="-s -w -X mindmory.local/core/internal/version.Value=$VERSION" -o "$stage/bin/$command" "./cmd/$command"; then
        :
      else
        echo "warning: garble failed for $command ($platform); falling back to stripped build"
        CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
          -ldflags="-s -w -X mindmory.local/core/internal/version.Value=$VERSION" -o "$stage/bin/$command" "./cmd/$command"
      fi
    else
      CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
        -ldflags="-s -w -X mindmory.local/core/internal/version.Value=$VERSION" -o "$stage/bin/$command" "./cmd/$command"
    fi
  done
  cp packaging/README.md packaging/AGENT_INSTALL.md packaging/mindmory-config.example.sh LICENSE NOTICE.md THIRD_PARTY_NOTICES.md THIRD_PARTY_LICENSES.txt packaging/setup.sh "$stage/"
  cp -R packaging/dsh "$stage/dsh"
  cp -R packaging/integrations "$stage/integrations"
  chmod 755 "$stage/setup.sh" "$stage/integrations/checkpoint-hook.sh" "$stage/dsh/checkpoint-relay.mjs"
  printf '%s\n' "$VERSION" > "$stage/VERSION"
  check_stage_clean "$stage" || exit 1
  tar -C "$DIST" -czf "$DIST/mindmory-mcp-$os-$arch.tar.gz" "mindmory-mcp-$os-$arch"
  echo "==> packaged mindmory-mcp-$os-$arch.tar.gz"
done

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$DIST" && sha256sum mindmory-mcp-*.tar.gz > SHA256SUMS)
else
  (cd "$DIST" && shasum -a 256 mindmory-mcp-*.tar.gz > SHA256SUMS)
fi
echo "==> release complete: $DIST"
trap - EXIT
rm -f "$license_check"
