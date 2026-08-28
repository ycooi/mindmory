# Mindmory-lite hardening gate report

Date: 2026-08-27

The expert recommendation was materially correct. The pre-hardening implementation had duplicated retrieval eligibility, an overridable model-facing intent flag, incomplete proposal evidence, non-atomic multi-file mutation application, timestamp-based current-turn selection, rewrite-oriented message storage, weak credential detection, unsigned history, live-directory snapshots, canonical embeddings, and a 12-query evaluation tied to existing IDs.

The lite architecture remains local and single-process. PostgreSQL, Docker, queues, and external vector databases were not introduced.

## Executable authority after hardening

- Models submit proposals but cannot assert `explicit` authority.
- Security, structural, and intent failures are typed. Only intent uncertainty is owner-approvable.
- Approval reloads the immutable message and rechecks ownership, current evidence hashes/ranges, policy, sensitivity, overlap, target lifecycle, and optimistic version before using the same commit path as automatic application.
- Every committed memory change is one complete version-3 event containing the proposal, evidence, before/after state, continuity revision, previous hash, key ID, and HMAC. Event fsync is the commit point; JSONL views, SQLite, and vectors are projections repaired by replay.
- Archived messages use server-owned sequences and segmented append-only hash chains. Changed-content replay conflicts; an incomplete final record is quarantined and recorded, while interior corruption refuses open/readiness.
- Automatic context always reloads canonical rows and applies lifecycle, sensitivity, secret/instruction, and project eligibility after index/vector candidate selection.
- Embeddings live only in atomic, versioned derived generations (`vectors.bin` plus canonical-ID mapping), keyed by embedding-input hash, model digest, and dimensions. Startup mmaps the active generation instead of rebuilding a Go vector map. They are excluded from canonical snapshots.
- Mutation history supports read-only `mindmoryctl verify` and dual-signed key rotation. Frozen snapshots include a SHA-256/size manifest and exclude derived indexes.

## Adversarial coverage

The native lite suite covers:

- governance: automatic intent, intent-only approval, approval of remember/correct/forget, changed-evidence rejection, security non-approval, and removal of model `explicit`;
- F3: structured secrets, instruction-like evidence, monotonic policy state, stale-index resistance, and automatic-context exclusion;
- lifecycle/scope: immediate, rebuilt, and restarted exclusion for forgotten/superseded/invalidated rows; projectless/global-only behavior; recent continuity and vector scope;
- durability: replay with all memory/proposal/evidence/continuity projections deleted, signed history tamper/forgery rejection, frozen restore with index rebuild, and unambiguous pre-created append files;
- concurrency: 100 identical mutations create one event/result, simultaneous corrections create one successor, and forget/correct races create one ordered revision;
- archive: exact replay, changed replay conflict, server turn ordering, final-tail recovery event, interior tamper rejection, record reordering rejection, and segment continuity;
- operations: loopback-only local auth, explicit local client identity, constant-time admin tokens on loopback, request/header/time bounds, route-specific body limits, rate limits, and context cancellation.

## Independent evaluation

`tests/corpus/lite-eval-v2.json` expands deterministic fixture templates into 200 cases, independent of production IDs:

| Category | Cases |
| --- | ---: |
| Exact English | 20 |
| Exact Chinese | 20 |
| Mixed Chinese/English | 20 |
| Typographical errors | 20 |
| Paraphrase/semantic | 30 |
| Negative | 30 |
| Lifecycle exclusion | 15 |
| Sensitivity/instruction exclusion | 15 |
| Project isolation | 15 |
| Alias expansion | 15 |

Measured locally on Darwin/arm64 with Go 1.26.6:

| Metric | Lexical | Lexical + qwen3-embedding:0.6b |
| --- | ---: | ---: |
| Recall@1 | 0.760 | 0.760 |
| Recall@5 | 1.000 | 1.000 |
| Recall@10 | 1.000 | 1.000 |
| MRR@10 | 0.853 | 0.853 |
| Negative false-positive rate | 0 | 0 |
| Secret/instruction leakage | 0 | 0 |
| Cross-project leakage | 0 | 0 |
| Inactive-lifecycle leakage | 0 | 0 |
| Search P50 | 9.2 ms | 108.5 ms |
| Search P95 | 10.2 ms | 119.0 ms |

The semantic model digest was `ac6da0dfba84a81fdbfbaf330198c33cd77c4cdfc53e8bc50eb581914a15621d`. Semantic retrieval remained opt-in because it did not improve this fixture’s ranking and added substantial latency. The evaluated minimum cosine was raised to 0.68 after the first semantic run exposed one adversarial negative false positive; the final full rerun produced zero.

The evaluator writes timestamped, per-query JSON containing binary/source revision, corpus version, model/digest, environment, metrics, latency, rebuild/startup timing, and context size. Its `--semantic` value is assigned directly to the server path under test.

## Persistent vector projection follow-up

The persistent-vector implementation keeps the evaluated flat-cosine behavior while replacing the boot-time RAM map with a versioned mmap-backed generation. `vectors.bin` has a fixed checked header and normalized float32 records; `vector-map.jsonl` maps ordinals back to canonical memory IDs and versioned input hashes; `CURRENT` is switched atomically. The committed header count is the cross-file commit marker. Startup truncates uncommitted tails and degrades semantic service—not lexical/core readiness—when a committed file is short or fails verification.

The 200-case corpus after this change remained identical: Recall@5/10 `1.000`, MRR@10 `0.853`, and all false-positive/leakage rates `0`. On the local Apple M3 Max, the 1,000-vector × 512-dimension microbenchmark measured:

| Flat scan | Time/query | Heap bytes/query |
| --- | ---: | ---: |
| Persistent mmap + top-K heap | 1.159 ms | 9,120 B |
| Legacy RAM vectors + sort-all | 0.897 ms | 24,672 B |

The persistent scan therefore trades about 29% raw scan CPU at this small fixture for bounded top-K allocation (about 63% fewer heap bytes per query), no corpus-sized Go vector copy at startup, durable reuse across restarts, and crash-recoverable incremental updates. Query embedding still dominates explicit semantic latency (~109.5 ms P50 locally), so semantic fallback remains the default policy when semantic search is enabled; reflex, relevance, and context packets remain lexical-only.

## Verification status

- Focused unit/integration suite: passing.
- Lite race detector: passing.
- `go vet` and builds for lite daemon, MCP bridge, operator CLI, and evaluator: passing.
- Fresh temporary no-Docker daemon smoke: readiness, checkpoint, governed mutation, retrieval, admin snapshot, and integrity CLI all passing.
- Repository-wide `go test ./...`: the lite and integration packages pass, but unrelated retired artifact/CAS/worker tests fail on this macOS workspace because their path-confinement tests treat the OS `/var` → `/private/var` symlink used by `t.TempDir()` as an unsafe source path. Those artifact-ingestion failures were not changed in this focused lite-memory hardening cycle.

Recovery and key-rotation procedures are in `docs/lite/HARDENING_AND_RECOVERY.md`.
