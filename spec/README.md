# Formal specs

Machine-checked statements about the analysis, written in [FSL](https://github.com/ymm-oss/fsl) and verified with `fslc`. They are documentation that cannot drift. `./verify.sh` re-runs them, and CI runs it on every push.

They check the **design**, not the implementation. Each one models a rule against what happens at run time. A counterexample means the rule reports an error that was never logged. Confirming that the Go code follows the rule takes the fixtures under `testdata/src/paths`.

## What is proved

| Spec | Claim |
| --- | --- |
| `fresh_value.fsl` | A value defined again after the log is a new value. A loop never matches one iteration's logged error with the next one's returned error. Every error logged and returned in one iteration is still reported |
| `phi_identity.fsl` | A φ-node the path has not resolved is compared by identity. The rule never reports an error that was not logged. A merge the walk enters after the log is still resolved and reported |
| `must_log.fsl` | A helper counts as logging only when every returning path logs its argument, or knows it is nil. Whenever that summary says "logs" and the error is not nil, the helper logged it |

Each of the three also witnesses what it gives up. `phi_identity.fsl` misses a report when the φ-node took the shared edge. `must_log.fsl` misses a recursive helper, whose recursive call reads an empty summary.

## The three that fail

These are not regressions. Each models the rule that the matching proved spec replaced, and its counterexample is the reason for the change.

| Spec | Rule it models | Counterexample |
| --- | --- | --- |
| `fresh_value_naive.fsl` | SSA values are compared, and nothing else | An error logged in one iteration matches the new error returned in the next. woodpecker's `cli/pipeline/purge.go` was reported for this |
| `phi_expand.fsl` | An unresolved φ-node stands for all of its edges | The new error is logged through the φ-node, and returning the shared error is reported. usememos/memos `store/attachment.go` was reported for this shape |
| `must_log_may.fsl` | A helper logs its argument when some path does | A helper that logs only under another condition logs nothing, and the caller is still reported |

## Running them

```bash
./verify.sh
```

The script reads each verdict from `fslc`'s JSON, never from its exit code. The three proved specs must be `proved` under induction. The three failing ones must be `violated` on the named invariant. A spec in the directory that is named in neither list fails the run.
