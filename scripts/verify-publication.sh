#!/bin/sh
# Audit tracked and staged repository content before publication.

set -eu

root="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
cd "$root"

status=0

for path in $(git ls-files); do
  case "$path" in
    .env|.env.*|var/*|archive/*|dist/*|packaging/dist/*|*.db|*.sqlite|*.jsonl|*.zstd|*.log|*.pid|scripts/import-agent-conversations.mjs)
      echo "publication audit: private/generated path is tracked: $path" >&2
      status=1
      ;;
  esac
done

# Personal machine paths and common credential assignments must never enter tracked text files.
# The policy detector and its test intentionally contain synthetic secret signatures so the
# product can recognize them; their behavior is covered by tests and they are excluded here.
if matches="$(git grep -nEI '(/Users/[^/[:space:]]+|/home/[^/[:space:]]+|BEGIN (RSA |OPENSSH |EC )?PRIVATE KEY|(^|[^A-Za-z])(api[_-]?key|access[_-]?token|auth[_-]?token|password)[[:space:]]*[:=][[:space:]]*[^$<{[:space:]][^[:space:]]*)' -- ':!scripts/verify-publication.sh' ':!internal/archive/policy_detect.go' ':!internal/archive/policy_detect_test.go' 2>/dev/null || true)"; then
  if [ -n "$matches" ]; then
    echo "publication audit: possible private path or credential found:" >&2
    printf '%s\n' "$matches" >&2
    status=1
  fi
fi

if [ "$status" -ne 0 ]; then
  echo "publication audit: FAILED — keep flagged content local" >&2
  exit 1
fi

echo "publication audit: OK — no forbidden tracked content detected"
