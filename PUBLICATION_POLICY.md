# Public publication policy

Mindmory is developed locally and published selectively. Public GitHub is a release channel, not
a mirror of the development machine.

## Default rule

All new files, data, experiments, reports, and changes are private by default. Content becomes
public only after it is complete, reviewed, intentionally selected for publication, and passes the
publication audit.

## Content allowed on public GitHub

- Release-ready source code and tests that use synthetic data
- Generic documentation and configuration examples without secrets or personal paths
- License, attribution, security, contribution, and release documentation
- Release artifacts produced by the approved packaging pipeline and privacy audit

## Content that must remain local

- User or assistant conversations, imported histories, memories, and prompts
- Runtime JSONL, SQLite databases, indexes, snapshots, proposals, logs, and diagnostics
- Tokens, keys, credentials, `.env` files, and machine-specific configuration
- Personal names or contact details not intentionally part of public attribution
- Home-directory paths, usernames, hostnames, device identifiers, and private repository URLs
- One-off importers, migration utilities, internal experiments, benchmarks with private data, and
  unpublished reports
- Draft or incomplete work that has not been approved for publication

## Required release procedure

1. Select only the finished content intended for the public repository or release.
2. Review the staged diff and repository status.
3. Run `sh scripts/verify-publication.sh`.
4. For binary releases, also run `sh scripts/verify-release.sh packaging/dist`.
5. Publish only after both the audit and explicit user authorization succeed.

The safe response to uncertainty is to leave the material untracked or ignored locally.
