// Package web holds the web UI: one HTML file with inline script, embedded into the dex binary.
package web

import _ "embed"

// Index is the web UI page.
//
//go:embed index.html
var Index []byte
