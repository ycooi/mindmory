# SQLite Read Projection

Mindmory separates durable authority from serving state:

```text
canonical append-only JSONL journals -> disposable SQLite read projection
                                      -> disposable vector projection
```

JSONL remains the portable recovery authority. SQLite is the complete local
read database: FTS candidate selection, full memory records, archived messages,
and evidence joins are served without opening JSONL on the request path.

## Read path

1. SQLite FTS5 selects policy-eligible memory IDs.
2. One batched SQLite query hydrates the complete candidate records.
3. Deterministic Go ranking applies the final policy and relevance ordering.
4. Exact recall reads the complete memory row from SQLite.
5. Evidence recall joins `evidence_current` to `messages_current` in SQLite.

The derived database stores complete JSON representations in addition to the
columns needed for filtering. This keeps schema evolution lossless while the
canonical Go record remains the single serialization contract.

## Write and recovery boundary

Canonical journals are committed and fsynced before their derived projection.
If projection refresh fails, the canonical event still exists and the SQLite
database can be rebuilt. The database uses WAL mode and is excluded from
canonical backups.

At startup, Mindmory validates canonical records and reconstructs a missing or
outdated projection. Deleting the derived directory is therefore a supported
recovery operation; canonical memory and evidence are not lost.

## Experimental low-RAM mode

All model-facing memory, message, search, recall, and evidence reads use
SQLite. Set `MINDMORY_LOW_RAM_EXPERIMENT=1` to release the archive-sized
memory, message, and evidence maps after the projection is verified. Mutations,
feedback, learner extraction, vector status, snapshots, shutdown, and restart
then use SQLite plus the canonical journals.

The mode remains experimental because startup still materializes canonical
files before releasing the maps. It therefore reduces steady-state heap, but
not peak startup memory. A future streaming/sequence-replay loader is required
to remove that final peak.

In the 100,000-memory probe on an Apple M3 Max, steady-state Go heap fell from
about 63.0 MB to 2.6 MB. Warm startup plus test shutdown increased from about
0.62 seconds to 0.98 seconds, primarily because the experiment verifies the
projection, releases maps, forces collection in the probe, and materializes
compatibility JSONL at shutdown. These figures are directional, not a hardware-
independent guarantee.

## Verification

The test suite covers:

- SQLite search and full-record reads after canonical in-memory maps are
  deliberately emptied.
- SQLite message and quoted-evidence reads without those maps.
- complete projection regeneration after deleting the derived directory.
- stale FTS candidates being rejected by the complete current-row projection.
- mutation, feedback, lifecycle, race, and MCP regression behavior.
- low-RAM governed remember/forget, learner, statistics, snapshot, shutdown,
  and restart behavior.

`BenchmarkSQLiteSearchAndRetrieve10K` measures FTS search plus complete record
retrieval over 10,000 memory rows without JSONL or Store maps in the timed path.
