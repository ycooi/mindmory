# Changelog

All notable changes to Mindmory are documented here. The project follows
[Semantic Versioning](https://semver.org/).

## [0.1.0] - 2026-08-28

### Added

- Local-first, single-process memory daemon backed by canonical JSONL and a
  rebuildable SQLite retrieval index.
- Lexical retrieval with optional local or OpenAI-compatible embeddings.
- Persistent vector generations with startup model-identity validation.
- MCP memory tools, agent-visible operational status, and incident guidance.
- Operator-controlled vector rebuild, integrity verification, snapshots, and
  configurable storage layout.

### Security

- Loopback-first operation, scoped MCP credentials, secret-aware ingestion,
  sanitized status output, and fail-closed vector mismatch handling.
