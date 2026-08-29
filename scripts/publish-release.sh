#!/bin/sh
# Publish Mindmory MCP binaries to a GitHub Release.
#
# Ships ONLY the compiled tarballs + SHA256SUMS from packaging/dist/ — never
# runtime data, credentials, tests, or private material. The release pipeline refuses to
# publish until scripts/verify-release.sh (host-side privacy audit) passes.
#
# Prerequisites:
#   - GitHub CLI:  gh auth login   (once)
#   - The public Mindmory source repository on GitHub.
#
# Usage:
#   sh scripts/publish-release.sh                # tag from packaging/VERSION
#   sh scripts/publish-release.sh --tag v0.1.1
#   sh scripts/publish-release.sh --repo yourname/mindmory-mcp
#   sh scripts/publish-release.sh --draft        # create as draft first
#   sh scripts/publish-release.sh --clobber      # replace an existing tag
#   sh scripts/publish-release.sh --dry-run      # audit + show notes, do NOT upload

set -eu

DIST="packaging/dist"
REPO=""
TAG=""
DRAFT=""
CLOBBER=""
DRY_RUN=0

usage() {
  sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//'
  exit 0
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --tag)     TAG="${2:-}"; shift 2 ;;
    --repo)    REPO="${2:-}"; shift 2 ;;
    --draft)   DRAFT="--draft"; shift ;;
    --clobber) CLOBBER="--clobber"; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    --help|-h) usage ;;
    *) echo "error: unknown option: $1" >&2; usage ;;
  esac
done

[ -d "$DIST" ] || { echo "publish: no $DIST — run 'make release' first" >&2; exit 1; }
tarballs="$(find "$DIST" -maxdepth 1 -name 'mindmory-mcp-*.tar.gz' | sort)"
[ -n "$tarballs" ] || { echo "publish: no tarballs in $DIST" >&2; exit 1; }

# ---------------------------------------------------------------------------
# 1. privacy gate — never publish an unaudited package
# ---------------------------------------------------------------------------
echo "==> privacy audit"
sh scripts/verify-release.sh "$DIST"

# ---------------------------------------------------------------------------
# 2. tag
# ---------------------------------------------------------------------------
if [ -z "$TAG" ]; then
  TAG="v$(cat "$DIST"/mindmory-mcp-linux-amd64/VERSION 2>/dev/null || cat VERSION)"
fi

notes() {
  cat <<EOF
## Mindmory MCP — local-first, evidence-backed memory for your AI assistant

Prebuilt binaries for the MIT-licensed Mindmory source repository. Runs entirely
on your own machine: no Docker, no PostgreSQL, no telemetry, no cloud. Your memories live
in \`var/data/\` as human-readable JSONL.

### What changed in v0.1.1

- SQLite now serves complete memory, message, and evidence records while JSONL
  remains the canonical, portable recovery source.
- Search candidate hydration and evidence retrieval stay inside SQLite instead
  of scanning or reopening the canonical archive.
- Optional \`MINDMORY_LOW_RAM_EXPERIMENT=1\` mode reduces steady-state Go heap
  by releasing archive-sized in-process maps after startup.

### Platforms

| Package | Platform |
| --- | --- |
| \`mindmory-mcp-darwin-amd64.tar.gz\` | macOS Intel |
| \`mindmory-mcp-darwin-arm64.tar.gz\` | macOS Apple Silicon |
| \`mindmory-mcp-linux-amd64.tar.gz\` | Linux x86_64 |
| \`mindmory-mcp-linux-arm64.tar.gz\` | Linux ARM64 |

### Quick start

\`\`\`bash
tar xzf mindmory-mcp-\$(uname -s | tr A-Z a-z)-\$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/').tar.gz
cd mindmory-mcp-*
./setup.sh
\`\`\`

\`setup.sh\` generates fresh per-instance secrets, starts the daemon, and prints
agent-specific Codex, Claude Code, DeepSeek Harness, and generic MCP guides
(\`~/.dsh/profiles/web/cordis.patch.yml\` and \`headless\`). The tools appear as
\`mcp__mindmory__*\` (memory_context, memory_search, memory_remember, ...).

### Verify downloads

\`\`\`bash
sha256sum -c SHA256SUMS
\`\`\`

### License

MIT License. Source, modification, and redistribution are permitted under the
terms in LICENSE.
EOF
}

if [ "$DRY_RUN" -eq 1 ]; then
  echo "==> dry run — would publish tag $TAG${REPO:+ to $REPO} with:"
  echo "$tarballs" | sed 's/^/    /'
  echo "$DIST/SHA256SUMS" | sed 's/^/    /'
  echo "==> release notes (draft):"
  notes
  exit 0
fi

command -v gh >/dev/null 2>&1 || { echo "publish: GitHub CLI (gh) not found" >&2; exit 1; }
gh auth status >/dev/null 2>&1 || { echo "publish: run 'gh auth login' first" >&2; exit 1; }

# ---------------------------------------------------------------------------
# 3. publish
# ---------------------------------------------------------------------------
repo_arg=""
[ -n "$REPO" ] && repo_arg="--repo $REPO"

echo "==> creating GitHub release $TAG${REPO:+ on $REPO}"
# shellcheck disable=SC2086
gh release create "$TAG" $tarballs "$DIST/SHA256SUMS" \
  $repo_arg $DRAFT $CLOBBER \
  --title "Mindmory MCP $TAG" \
  --notes "$(notes)"

echo "==> published. Download link: https://github.com/${REPO:-<repo>}/releases/tag/$TAG"
