#!/usr/bin/env bash
# Verify every FSL spec. Four of them model the designs the analysis replaced.
# Each holds a reachable whose witness is the false report that the old rule
# makes. See spec/README.md.
#
# Every verdict is read from the JSON `result`, never from the exit code. fslc
# exits non-zero for a parse error and an internal error as well as for a
# violation. The result also tells a proof apart from a bounded check.
#
# fslc is the FSL verifier: https://github.com/ymm-oss/fsl
set -o pipefail

cd "$(dirname "${BASH_SOURCE[0]}")" || exit 1

if ! command -v fslc > /dev/null 2>&1; then
  echo "fslc not found; skipping the specs. Install it with: mise install (mise.toml pins its version)" >&2
  exit 0
fi

## Reads one top-level string field out of fslc's JSON, without needing jq.
field() {
  sed -n "s/.*\"$2\"[[:space:]]*:[[:space:]]*\"\([A-Za-z_]*\)\".*/\1/p" <<< "$1" | head -n 1
}

## These must be inductive rather than true only to a depth.
proving=(fresh_value phi_identity must_log loop_exit)
## These model replaced rules. They hold only reachables, so there is nothing to
## prove by induction. A bounded run must witness every reachable.
bounded=(fresh_value_naive phi_expand must_log_may loop_exit_naive)

status=0

## Every spec in the directory must be named above, or nothing would run it.
for f in *.fsl; do
  name=${f%.fsl}
  if [[ " ${proving[*]} ${bounded[*]} " != *" $name "* ]]; then
    echo "  FAILED   $f is named in neither list, so nothing verifies it" >&2
    status=1
  fi
done

for f in "${proving[@]}"; do
  # The depth bounds the search for each reachable's witness. loop_exit's
  # genuine report takes nine steps.
  out=$(fslc verify "$f.fsl" --engine induction --depth 16 2>&1)
  if [[ "$(field "$out" result)" == "proved" ]]; then
    echo "  ok       $f.fsl (proved)"
  else
    echo "  FAILED   $f.fsl is $(field "$out" result), want proved" >&2
    status=1
  fi
done
for f in "${bounded[@]}"; do
  out=$(fslc verify "$f.fsl" --depth 8 2>&1)
  if [[ "$(field "$out" result)" == "verified" ]]; then
    echo "  ok       $f.fsl (verified to depth 8)"
  else
    echo "  FAILED   $f.fsl is $(field "$out" result), want verified" >&2
    status=1
  fi
done
exit $status
