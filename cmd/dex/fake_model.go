//go:build !dex_e2e

package main

import (
	"context"

	"github.com/recally-io/dex/internal/loginenv"
)

func testFakeModel() bool { return false }

func testLoginEnv() func(ctx context.Context, base map[string]string) (map[string]string, error) {
	return loginenv.Environment
}
