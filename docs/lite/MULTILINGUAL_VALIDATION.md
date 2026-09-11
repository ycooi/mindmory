# Multilingual retrieval validation

The v0.2.1 regression gate runs with:

```sh
go test ./internal/lite -run '^TestMultilingual100$' -count=1 -v
```

The shared store contains ten synthetic target subjects, ten shorter-name
distractors, and thirty excluded records (secret, retired, and another project).
Neither query nor memory text contains its fixture identifier. A fixed overlay
vocabulary is defined independently of the sentence queries.

| Query class | Cases | Required outcome |
| --- | ---: | --- |
| Exact Chinese name | 10 | Target first |
| English alias | 10 | Target first |
| Chinese sentence | 10 | Target first |
| English sentence | 10 | Target first |
| Punctuation | 10 | Target first |
| Mixed language | 10 | Target first |
| Long contextual sentence | 10 | Target first |
| Two independently named entities | 10 | Both in top five |
| Unrelated negative | 10 | No results |
| Case and surrounding whitespace | 10 | Target first |

All cases reject excluded records and require stable repeated results. With the
original matching implementation, 70 cases pass and 30 fail. With the corrected
implementation, all 100 pass. This comparison holds the new overlay loader and
fixture constant and changes only the matching implementation.

Additional tests cover occurrence overlap and order, punctuation boundaries,
CJK aliases to English records through the HTTP search route, built-in behavior,
configuration path resolution, missing/malformed/oversized files, duplicate
merging, and repeat loading without modifying the default table.

These targeted tests do not establish unrestricted translation or measured
quality on private stores. The older lite-eval-v2 fixtures share numeric anchors
between queries and memories; their alias cases inject matching vocabulary.
Retain their scores as identifier/plumbing checks, not multilingual coverage.
The separate semantic challenge also remains part of the full test suite.

The two-rune CJK prefix fallback remains unchanged. Filtering function words
requires separate ranking evidence that preserves meaningful short names.
General candidate provenance and expansion-order ranking changes are outside
this corrective release.

Release verification also runs the packaged native daemon and MCP bridge against
an isolated synthetic store. MCP initialization, tool discovery, memory creation,
and searches for an embedded Chinese name and an English alias pass with the
overlay loaded from the default data-directory path at process startup.
