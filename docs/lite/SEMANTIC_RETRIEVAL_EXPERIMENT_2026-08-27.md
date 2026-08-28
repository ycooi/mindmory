# Semantic retrieval experiment — 2026-08-27

## Question

Can Qwen3 semantic fallback improve Mindmory Lite retrieval without regressing established lexical recall, policy isolation, false-positive behavior, or local latency?

## Method

All runs used the real Lite store/server/vector path on Darwin arm64 with Ollama. Production defaults were not changed. The evaluator gained explicit experiment controls for query instruction, cosine threshold, confidence margin, semantic-only ranking, vector-first ranking, weighted reciprocal-rank fusion, top-one rescue, and fallback triggering.

Two corpora were used:

- `lite-eval-v2.json`: the established 200-case regression suite.
- `lite-semantic-challenge-v1.json`: a new 80-case suite containing 30 English/cross-language semantic positives with no visible fixture ordinals, 20 unrelated negatives, and 30 lifecycle/sensitivity/project exclusions.

The challenge corpus was necessary because the established suite already had Recall@5/10 of 1.000 and its generated numeric suffixes created lexical shortcuts between unrelated fixtures.

## Main results

| Corpus / configuration | R@1 | R@5 | R@10 | MRR@10 | Negative FPR | Leakage | P50 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Challenge, lexical | 0.000 | 0.133 | 0.167 | 0.051 | 0 | 0 | 6.1 ms |
| Challenge, current semantic behavior | 0.000 | 0.167 | 0.200 | 0.059 | 0 | 0 | 109.0 ms |
| Challenge, pure 0.6B vector, no cutoff | 0.400 | 0.767 | 0.900 | 0.557 | 1.000 | 0 | — |
| Challenge, instructed 0.6B vector, no cutoff | 0.400 | 0.833 | 0.900 | 0.565 | 1.000 | 0 | 108.2 ms |
| Challenge, instructed 0.6B, calibrated top-one rescue | 0.333 | 0.400 | 0.400 | 0.353 | 0 | 0 | 107.0 ms |
| Established suite, lexical | 0.760 | 1.000 | 1.000 | 0.853 | 0 | 0 | 9.7 ms |
| Established suite, calibrated top-one rescue | 0.824 | 1.000 | 1.000 | 0.881 | 0 | 0 | 111.2 ms |

The calibrated top-one configuration used:

```text
model: qwen3-embedding:0.6b
query instruction: Retrieve the personal memory that best answers the user's query.
minimum candidate score: 0.36
automatic high-confidence score: 0.50
otherwise required top1-top2 margin: 0.06
merge: admit at most one accepted semantic candidate before weak lexical results
mode: SEMANTIC_FALLBACK
```

It is the only tested merge that improved established-suite top-1/MRR while preserving Recall@5/10, zero false positives, and zero policy leakage.

## Diagnostic findings

1. The existing `0.68` cutoff is too high for the current Ollama/Qwen representation. On the challenge set, correct expected-hit scores had median `0.571` without instruction and `0.456` with instruction. Most useful candidates were rejected before ranking.
2. A low absolute cutoff alone is unsafe. `0.36` produced zero challenge false positives but 100% false positives on the established negative cohort. Score calibration does not transfer reliably between these two synthetic distributions.
3. Query instruction improved raw 0.6B Recall@5 from `0.767` to `0.833`, but shifted the cosine distribution downward. Instruction and threshold must therefore be versioned and calibrated together.
4. The current hybrid match-class merge hides embedding quality. Pure vector retrieval was useful, while current hybrid retrieval remained near lexical baseline because weak lexical candidates outranked vector-only candidates.
5. Weighted RRF recovered Recall@5 but reduced top-1 because weak lexical corroboration sometimes boosted the wrong semantic candidate.
6. A single semantic rescue candidate was safer than full vector-first or RRF merging. It cannot push several valid lexical candidates out of the result window.
7. The locally installed `qwen3-embedding:4b-fp16` did not justify its cost: challenge R@5/R@10 were `0.767/0.867`, versus `0.833/0.900` for instructed 0.6B, while P50 rose from about `108 ms` to `122 ms`.
8. The remaining practical problem is fallback triggering. On the established suite, semantic inference ran for paraphrase and typo cases but also for negative and policy-excluded queries, pushing overall P50 above 100 ms. Strict fallback only when lexical returned no hits preserved all established metrics and latency for lexical hits, but challenge R@5 improved only from `0.133` to `0.200`.

## Decision

Do not enable the experimental semantic policy as the general production default yet. The experiment proves that the 0.6B embeddings contain useful retrieval signal and that query instruction plus top-one rescue can improve ranking safely on these fixtures. It does not yet prove that the approximately 100 ms query-embedding cost and cross-corpus calibration are acceptable for real usage.

Recommended next gate:

1. collect an anonymized, owner-labeled set of real lexical misses and irrelevant queries;
2. distinguish informative weak lexical matches from numeric/common-token noise;
3. invoke semantic fallback only for that calibrated ambiguity class;
4. retain top-one rescue, final canonical eligibility, and the high-score-or-margin confidence gate;
5. require zero FPR/leakage and no Recall@5/10 regression on both frozen suites before changing defaults.

The experiment output files were written under `/tmp/mindmory-semexp-*.json` on the test machine and include per-query rankings, match strengths, latency, model digest, and configuration metadata.
