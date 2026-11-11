//go:build dex_e2e

package main

import "context"

// Test builds use the native runtime with its faux provider.
func testFakeModel() bool { return true }

// Test builds keep the explicit tool environment, so a developer's shell cannot change a test.
func testLoginEnv() func(ctx context.Context, base map[string]string) (map[string]string, error) {
	return nil
}
