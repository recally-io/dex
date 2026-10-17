// Package agent owns agent state and paths.
package agent

import "path/filepath"

// Home returns the storage root for an agent in the resolved data directory.
func Home(dataDir, id string) string {
	return filepath.Join(dataDir, "agents", id)
}
