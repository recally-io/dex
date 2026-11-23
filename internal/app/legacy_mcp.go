package app

import (
	"log/slog"
	"os"
	"path/filepath"
)

func warnLegacyMCPFiles(dataDir string) {
	warn := func(path string) {
		if _, err := os.Lstat(path); err == nil {
			slog.Warn("dex ignores legacy mcp.json", "path", path)
		}
	}
	warn(filepath.Join(dataDir, ".agents", "mcp.json"))
	agentsDir := filepath.Join(dataDir, "agents")
	entries, _ := os.ReadDir(agentsDir)
	for _, entry := range entries {
		warn(filepath.Join(agentsDir, entry.Name(), ".agents", "mcp.json"))
	}
}
