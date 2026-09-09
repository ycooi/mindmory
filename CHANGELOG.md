# Changelog

All notable changes to Mindmory are documented here. The project follows
[Semantic Versioning](https://semver.org/).

## [0.2.0] - 2026-09-09

### Added

- Native Porter-tokenized FTS5 and BM25 retrieval alongside the existing
  trigram/CJK projection.
- Reproducible short, medium, long, and superlong synthetic scale evaluation,
  plus support for the public coding-agent-life-v1 fixture format.
- `mindmoryctl providers certify [--probe]` for validating retrieval-provider
  authority, disclosure behavior, identity, determinism, and dimensions.

### Changed

- Lexical search now uses identifier-aware ranking, bounded candidate
  hydration, stable tie-breaking, and safe fallback for ordinal anchors.
- MCP search responses omit duplicate snippets when compact content is
  available, reducing retrieval-context token use.
- SQLite uses a bounded WAL connection pool and a resource-controlled,
  set-based index reconstruction path. The replacement commits atomically,
  and concurrent searches continue against the prior index generation.
- DeepSeek Harness checkpoint relay tests use deadline-based readiness and
  shutdown handling for more reliable slow-host execution.

### Fixed

- Healthy hard-negative queries no longer fall back to scanning the canonical
  archive.
- BM25 ordering and score saturation now preserve useful relevance separation.
- Embedding providers fail closed before canonical-derived text is submitted
  when their authority or disclosure contract is invalid.

### Security

- Retrieval providers now declare whether they are local or remote and whether
  content is disclosed, while canonical storage authority remains exclusively
  with Mindmory.

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
