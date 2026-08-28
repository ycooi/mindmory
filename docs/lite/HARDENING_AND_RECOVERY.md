# Mindmory-lite authority and recovery

Mindmory-lite has two canonical append-only histories:

- `memory_events.jsonl`: complete, HMAC-signed governed memory mutations.
- `messages/messages-*.jsonl`: immutable archived messages with server sequence and a SHA-256 record chain.

`memories.jsonl`, `proposals.jsonl`, `evidence.jsonl`, and `continuity.jsonl` are compact projections reconstructed from memory events. The default derived directory is `var/derived`: it contains `index.db` and versioned vector generations under `vectors/`. `vectors/CURRENT` atomically selects an mmap-backed generation containing `manifest.json`, `vectors.bin`, and `vector-map.jsonl`. Embeddings are keyed by canonical memory ID, versioned embedding-input hash, model digest, and dimensions. Derived files are excluded from canonical snapshots and may be deleted and rebuilt.

Local vector operations (with the daemon stopped) are:

```sh
./bin/mindmoryd-lite --sync-vectors
./bin/mindmoryd-lite --vector-status
./bin/mindmoryd-lite --verify-vectors
```

`--embed` remains a deprecated alias for `--sync-vectors`. `MINDMORY_DERIVED_DIR` and `MINDMORY_VECTOR_DIR` override the derived locations; neither may point at the canonical data root.

Embeddings are optional. Set `MINDMORY_EMBED_PROVIDER=disabled` on a machine without Ollama;
lexical search and canonical persistence continue to work. Supported providers are `ollama` and
`openai-compatible`. A non-loopback endpoint additionally requires HTTPS,
`MINDMORY_EMBED_ALLOW_REMOTE=1`, and `MINDMORY_EMBED_API_KEY`. Remote embedding sends memory and
query text to that endpoint. The full path layout is controlled by `MINDMORY_ROOT_DIR`,
`MINDMORY_DATA_DIR`, `MINDMORY_DERIVED_DIR`, `MINDMORY_VECTOR_DIR`,
`MINDMORY_SNAPSHOT_DIR`, and `MINDMORY_EXPORT_DIR`.

## Startup incidents and MCP notification

Startup computes one cached system state (`READY`, `DEGRADED`, or `ACTION_REQUIRED`). Model
identity comparison is model-agnostic: any change to the declared model name, digest, configured
dimensions, embedding-input version, or normalization version produces
`EMBEDDING_MODEL_MISMATCH`. The event is written to the structured daemon log and `ops.jsonl`, and
is returned by `GET /v1/system/status` and the `mindmory_status` MCP tool. Ordinary MCP tools are
quarantined while action is required, so the normal conversation-start `memory_context` call gives
the agent the warning and exact operator commands. Canonical and lexical initialization checks are
not repeated per MCP call.

The agent cannot authorize remediation. The owner runs the incident-bound command shown in status:

```bash
mindmoryctl vectors status
mindmoryctl vectors rebuild --incident-id inc_EXAMPLE --confirm
```

The daemon rejects missing, resolved, or changed incident identifiers and concurrent rebuilds.
Rebuilds populate and verify a detached `BUILDING` generation before atomically updating `CURRENT`;
the old `READY` generation remains selected after provider failure, interruption, or validation
failure.

### Read-only agent information

The authenticated `GET /v1/system/status` endpoint and `mindmory_status` MCP tool expose the same
sanitized view: configured storage folders, embedding endpoint/path/model/digest/dimensions,
remote-provider and semantic flags, initialization state, incidents, record counts, proposal
counts, evidence-link counts, continuity/project counts, and persistent-vector statistics. Counts
are refreshed cheaply for each status request; startup integrity/model checks remain cached. The
view never includes credentials, signing material, canonical record content, queries, evidence
quotes, or other memory content. The HTTP status route is GET-only and the MCP tool has no mutation
path.

## Verify before backup or repair

Run the read-only verifier with the daemon stopped or against a frozen snapshot:

```sh
set -a; . ./mindmory-config.sh; set +a
./bin/mindmoryctl verify --data-dir var/data
```

It verifies event sequence, hash chain, HMAC/key ID, key-rotation bridge, message segment order, message sequence, exact content hashes, and message record hashes. It never truncates or repairs files. A nonzero exit means the directory must not be used for automatic context.

## Frozen backup and restore drill

Create a snapshot through `POST /v1/admin/snapshot` with `X-Admin-Token`, then copy the returned snapshot directory—not the live data directory. Every included canonical file has a size and SHA-256 entry in `manifest.json`; derived indexes and vectors are excluded.

For a restore drill:

1. Copy the frozen snapshot to a new temporary data directory.
2. Verify it with `mindmoryctl verify` and the current signing key.
3. Start `mindmoryd-lite` against the temporary directory. Event replay rebuilds canonical projections and SQLite. Lexical search is immediately available even when no vector generation is restored.
4. Run project-isolation, forgotten-memory, and secret-leakage retrieval smoke tests.
5. Destroy only the temporary drill directory after recording the result.

## Signing-key rotation

Keep the daemon stopped and provide both the current key and a new random key:

```sh
set -a; . ./mindmory-config.sh; set +a
MINDMORY_NEW_CURSOR_SIGNING_KEY='<new-key-at-least-32-bytes>' \
  ./bin/mindmoryd-lite --rotate-integrity-key
```

The daemon appends `key_rotations.jsonl` record signed by both keys and anchored to the current event head. Replace `MINDMORY_CURSOR_SIGNING_KEY` with the new key immediately, securely retain an offline recovery copy, and run `mindmoryctl verify`. New events must use the new key; the dual-signed bridge lets the new key authenticate the previous hash-chained history without keeping the retired key online.

## Lost-key recovery

The signing key cannot be reconstructed from the data. If the current key is lost, do not bypass verification or manufacture a replacement history. Restore the newest frozen snapshot for which its signing key is available. If no matching key exists, preserve the unreadable directory as evidence, create a fresh data directory and key, and re-import only material independently verified by the owner. Such re-imported data is new history, not a continuation of the unverifiable chain.

## Corruption behavior

- A single incomplete final message record is quarantined and the segment is truncated to the last complete fsynced line at daemon startup.
- An invalid interior message record, segment gap, record-chain break, event-chain break, invalid HMAC, or missing rotation anchor refuses readiness/open.
- Mutation projection failures after the canonical event fsync are repaired by replay; they do not create a second mutation.
- Uncommitted vector or mapping tails are truncated to the header's committed count. A file shorter than its committed count is marked degraded; canonical and lexical readiness remain healthy.
