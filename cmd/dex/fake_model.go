//go:build !dex_e2e

package main

import (
	"context"

	"github.com/recally-io/dex/internal/engine"
)

func testFakeModel() bool { return false }

func testLoginEnv() func(context.Context) (map[string]string, error) { return engine.LoginShellEnv }
