#!/bin/sh
# Host-side privacy audit for Mindmory MCP release tarballs.
#
# Runs on the BUILD HOST (not inside the build container) so it can compare
# the tarballs against the real host identity: home directory, user name,
# hostname, and the repository's git remote. It fails the release if:
#
#   - any file outside the package allowlist is present,
#   - runtime secrets, var/ data, .git history, Go source, or docs/tests shipped,
#   - any staged file or binary contains a host-derived private marker
#     (home path, user, hostname, git remote) or an explicit PRIVATE_MARKERS
#     string (space-separated, e.g. "alice 86c68edb...").
#
# Usage: sh scripts/verify-release.sh [packaging/dist]

set -eu

DIST="${1:-packaging/dist}"
[ -d "$DIST" ] || { echo "verify-release: no such dir: $DIST" >&2; exit 1; }

tarballs="$(find "$DIST" -maxdepth 1 -name 'mindmory-mcp-*.tar.gz' | sort)"
[ -n "$tarballs" ] || { echo "verify-release: no tarballs found in $DIST" >&2; exit 1; }

# ---- derive host private markers ----------------------------------------
markers=""
homebase="$(basename "${HOME:-}")"
[ -n "$homebase" ] && [ "$homebase" != "/" ] && markers="$markers $homebase"
[ -n "${USER:-}" ] && markers="$markers $USER"
[ -n "${LOGNAME:-}" ] && [ "${LOGNAME:-}" != "${USER:-}" ] && markers="$markers $LOGNAME"
hostname="$(hostname 2>/dev/null || true)"
[ -n "$hostname" ] && markers="$markers $hostname"
if remote="$(git -C "$(dirname "$0")/.." remote get-url origin 2>/dev/null || true)"; then
  markers="$markers ${remote#https://} ${remote#git@}"
fi
markers="$markers ${PRIVATE_MARKERS:-}"

allow="bin/mindmoryd-lite bin/mindmoryctl bin/mindmory-mcp-stdio mindmory-config.example.sh AGENT_INSTALL.md README.md LICENSE NOTICE.md THIRD_PARTY_NOTICES.md THIRD_PARTY_LICENSES.txt VERSION setup.sh dsh/README.md dsh/cordis.patch.example.yml integrations/README.md integrations/checkpoint-hook.sh integrations/codex/README.md integrations/codex/config.toml.example integrations/codex/hooks.json.example integrations/claude-code/README.md integrations/claude-code/mcp.json.example integrations/claude-code/settings.json.example integrations/generic/README.md"

status=0
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

for tb in $tarballs; do
  name="$(basename "$tb" .tar.gz)"
  echo "==> auditing $name"
  rm -rf "$tmp/$name"
  mkdir -p "$tmp/$name"
  tar -xzf "$tb" -C "$tmp/$name" --strip-components=1

  # 1. allowlist + forbidden content (checked on allowlisted files only)
  for f in $(find "$tmp/$name" -type f | sort); do
    rel="${f#"$tmp/$name"/}"
    case " $allow " in
      *" $rel "*) ;;
      *)
        echo "   LEAK: unexpected file: $rel" >&2
        status=1
        continue
        ;;
    esac
    for pat in '\.env$' 'var/' '\.git/' '\.go$' 'testdata' '/docs/'; do
      if printf '%s' "$rel" | grep -qE "$pat"; then
        echo "   LEAK: forbidden pattern '$pat': $rel" >&2
        status=1
      fi
    done
  done
  # A runtime .env is never allowed.
  if [ -f "$tmp/$name/.env" ]; then
    echo "   LEAK: real .env shipped" >&2; status=1
  fi

  # 3. private markers in binaries and text
  for m in $markers; do
    [ -n "$m" ] || continue
    if strings "$tmp/$name"/bin/* 2>/dev/null | grep -qF "$m"; then
      echo "   LEAK: marker '$m' in binaries" >&2; status=1
    fi
    if find "$tmp/$name" -type f ! -path '*/bin/*' -exec grep -lF "$m" {} + 2>/dev/null | grep -q .; then
      echo "   LEAK: marker '$m' in package files" >&2; status=1
    fi
  done

  if [ "$status" -eq 0 ]; then
    echo "   OK: allowlist clean, no forbidden content, no private markers"
  fi
done

if [ "$status" -ne 0 ]; then
  echo "verify-release: FAILED — do not distribute these tarballs" >&2
  exit 1
fi
echo "verify-release: all tarballs clean"
