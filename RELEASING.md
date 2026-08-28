# Releasing Mindmory

1. Confirm `VERSION`, `CHANGELOG.md`, `CITATION.cff`, and
   `internal/version/version.go` contain the same semantic version.
2. Regenerate `THIRD_PARTY_LICENSES.txt` with
   `sh scripts/generate-third-party-licenses.sh` and review any dependency
   license changes.
3. Run the complete test and privacy gates in a clean checkout.
4. Build release binaries with `-trimpath`; produce `SHA256SUMS` and retain the
   source commit identifier used for the build.
5. Create a signed Git tag such as `v0.1.0` when a signing identity is
   available.
6. Publish the source tag and corresponding checksummed binaries together.
7. Preserve the GitHub release and build logs as the release record.

Signed commits or tags, source history, checksums, and release provenance are
the ownership and integrity evidence. Do not add hidden or random fingerprints
to source files.
