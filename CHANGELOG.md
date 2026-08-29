# Changelog

All notable changes to Mindmory are documented here. The project follows
[Semantic Versioning](https://semver.org/).

## [0.1.2] - 2026-08-29

### Fixed

- Codex and Claude Code integrations now archive the completed assistant
  response through their `Stop` lifecycle hook, in addition to the existing
  exact user-prompt checkpoint.
- Assistant archive records preserve role, host identity, content, turn order,
  canonical message-journal integrity, and the complete SQLite projection.
- Hook retries with the same external event no longer conflict solely because
  their client-supplied occurrence timestamp changed.

### Added

- Role-aware generic checkpoint adapter support for `UserPromptSubmit` and
  `Stop` events.
- Regression coverage for assistant persistence, restart, low-RAM mode,
  idempotency, and release hook templates.

## [0.1.1] - 2026-08-29

### Added

- Complete disposable SQLite read projection for memories, messages, and
  evidence, while canonical JSONL remains the portable recovery authority.
- Opt-in `MINDMORY_LOW_RAM_EXPERIMENT=1` mode that releases archive-sized Go
  maps after startup and serves operational reads from SQLite.
- Reproducible SQLite retrieval benchmarks and a 100,000-memory heap probe.

### Changed

- Search candidates are hydrated in a single SQLite batch; exact recall,
  evidence joins, learner input, statistics, and vector health use SQLite.
- Evidence rows no longer duplicate complete archived message bodies.

### Fixed

- Feedback access counts are no longer incremented twice at checkpoint.
- MCP status in low-RAM mode computes vector freshness in SQL without loading
  the complete archive.
- Packaged setup keeps the operator CLI endpoint aligned when a custom daemon
  port is selected.

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
