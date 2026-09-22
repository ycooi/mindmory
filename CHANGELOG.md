# Changelog

All notable changes to Mindmory are documented here. The project follows
[Semantic Versioning](https://semver.org/).

## [0.2.2] - 2026-09-22

### Changed

- Add zero-schema Codex and DeepSeek Harness native paths. `UserPromptSubmit` now checkpoints the
  prompt under its real Codex session/project, requests strict relevance, and
  injects at most three memories in a 320-character, 80-estimated-token
  plain-text packet. `Stop`
  checkpoints the assistant response without adding model context.
- Reject weak heat-dominated fuzzy matches from automatic native injection;
  explicit search remains available for broader discovery. Native relevance
  does not warm activation or expose IDs, scores, provenance, or wire JSON.
- Make MCP optional for Codex and document `mcp_servers.mindmory.enabled=false`
  as the zero-fixed-token configuration. The compact MCP gateway remains the
  compatibility path for other hosts; evidence-backed mutations require its
  checkpoint adapter and static bridge to share the same bound session.
- Replace DeepSeek Harness's MCP, eager session reflex, separate relevance, and
  checkpoint-only rows with one native Cordis relay. It injects the same strict
  320-character/80-estimated-token packet, records it in Harness history,
  archives assembled replies, and scrubs credential-like child environment
  variables.
- Normalize passive-learner intent cues before matching so capitalized English
  statements such as `Remember that ...` follow the same governed path as
  lowercase and CJK cues.
- Make the token-efficient `compact` MCP profile the default: one `mindmory`
  gateway advertises all existing operations through `action` and `args`.
  Set `MINDMORY_MCP_PROFILE=full` only for compatibility with clients that
  require the former thirteen separate tool names.
- Stop instructing agents to fetch a context packet at every session start.
  Retrieval is now on demand, and callers are told to keep `limit` and
  `max_chars` small.
- Bound compact-profile payload defaults to four search hits, 1,200 context
  characters, ten list/journal rows, and 2,000 artifact characters. Larger
  reads remain available only through an explicit caller override.
- Recognize a narrowly bounded natural-language allocation paraphrase in
  strict automatic relevance without enabling semantic embeddings or broad
  thesaurus expansion.

### Fixed

- Preserve access heat, last-use sequence, and feedback state when the signed
  mutation journal rebuilds a matching governed memory version at startup.
  Runtime state from stale or governed-content-mismatched projections remains
  rejected.
- Ignore malformed DeepSeek Harness event timestamps instead of allowing an
  invalid date to throw inside the relay callback.
- Keep generic and Claude Code compatibility checkpoints on the unscoped
  session bound to the MCP bridge, preventing host `cwd` metadata from breaking
  evidence-backed mutation authority. Native hooks retain their real project
  and per-host session isolation.

### Validation

- Add Codex and DeepSeek Harness native-hook functional tests for relevant, irrelevant, failed, assistant,
  Unicode, bounded-output, and 100-turn synthetic workloads, plus strict-match
  filtering tests and checkpoint-versus-native benchmarks.
- Add a fixed schema-size budget for the compact profile and verify that the
  full compatibility profile remains available with unchanged operation
  semantics and server-injected mutation authority.
- Add compact-versus-full dispatch benchmarks for memory search and context
  calls so the gateway's latency and allocation overhead remain measurable.
- Add repeated verified-restart coverage for runtime ranking and feedback
  durability, including rejection of heat copied from a mismatched projection.

## [0.2.1] - 2026-09-11

### Fixed

- Recover CJK canonical names embedded in sentences; resolve overlapping names
  by occurrence so separately mentioned shorter names remain searchable.
- Match unspaced CJK alias phrases inside longer queries.
- Load the optional alias overlay at daemon startup through `MINDMORY_ALIAS_FILE`,
  defaulting to the configured data directory's `aliases.json`. Invalid files
  retain built-ins and produce a warning without exposing file contents.
- Limit reverse paraphrase expansion to standalone canonical names to avoid
  flooding sentence queries with weak alternatives.

### Validation

- Add 100 synthetic retrieval cases across ten query classes, without fixture
  identifiers in memory or query text, plus alias configuration and HTTP tests.
- Clarify that the older ordinal-based corpus measures identifier retrieval
  and injected-alias plumbing, not general multilingual or paraphrase quality.
- Thanks to Ember for reporting and investigating the multilingual defects.

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
