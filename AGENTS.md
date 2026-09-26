# Repository instructions

errlogreturn is a Go analyzer that reports errors logged and returned along one path. Prefer silence when evidence is incomplete: a false report is more costly than a missed duplicate log. The detailed analysis, its rejected designs, and test cases are recorded in [implementation notes](design/implementation.md). Read the relevant section before changing detection, diagnostics, or fixes.

Run `go test ./...` for Go changes and `./test_all.sh` before claiming the full repository gate passes. Keep the README and analyzer behavior in sync.
