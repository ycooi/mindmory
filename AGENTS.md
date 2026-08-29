# Repository publication rules

These rules apply to every human or agent working in this repository.

1. Treat all new material as local/private until it is deliberately reviewed and declared ready
   for public release.
2. Never push to a public remote, create a GitHub release, or publish an artifact unless the user
   explicitly requests that publication.
3. Public GitHub content may contain only reviewed source code, tests using synthetic data,
   generic documentation, examples without credentials, licenses, and verified release artifacts.
4. Conversation histories, imported messages, memories, runtime databases, JSONL stores,
   snapshots, logs, credentials, tokens, local configuration, personal paths, host details,
   private reports, and one-off migration/import tools must remain local.
5. Before any push or GitHub release, run `sh scripts/verify-publication.sh`. A failed audit blocks
   publication until the flagged content is removed or explicitly converted into safe generic
   material.
6. Do not bypass the publication audit with `--no-verify`. If uncertain, keep the content local.

See `PUBLICATION_POLICY.md` for the complete policy.
