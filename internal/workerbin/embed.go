// Package workerbin holds the compiled Bun worker, gzipped. `mise run build:worker` writes
// dex-worker.gz for the target platform; go build fails without it.
package workerbin

import _ "embed"

//go:embed dex-worker.gz
var Gzip []byte
