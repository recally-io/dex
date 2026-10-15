//go:build dex_e2e

package main

// Test builds run the real worker with its faux provider.
func testWorkerEnv() []string { return []string{"DEX_FAKE_MODEL=1"} }
