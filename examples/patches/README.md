# WPT Patch Overrides

Store patch files here when upstream WPT tests require adjustments to run
inside Sobek. Add the patch path to the corresponding entry in `wpt.json`
and run `go run ./cmd/sync-wpt` to re-apply the changes after syncing new
tests.

Patches should be created with `git diff` from the repository root to keep
paths consistent with `git apply`.

