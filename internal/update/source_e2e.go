//go:build dex_e2e

package update

import "os"

// Only test builds can select a local release fixture.
func releaseBase() string { return os.Getenv("DEX_TEST_RELEASE_API") }
