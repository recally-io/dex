package engine

import (
	"context"
	"maps"
)

// MarkStale requests an idle reload. Agents that have not opened load the current
// configuration on their first open.
func (a *Agents) MarkStale(agentID, reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || (a.resources[agentID] == nil && a.opening[agentID] == nil) {
		return
	}
	a.dirty[agentID] = reason
	if a.busy[agentID] == 0 {
		a.wakeReloads()
	}
}

func (a *Agents) markExtensionChange(agentID, scope, reason string) {
	if scope != "dex" {
		a.MarkStale(agentID, reason)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	for id := range a.resources {
		if agentIDPattern.MatchString(id) {
			a.dirty[id] = reason
		}
	}
	for id := range a.opening {
		if agentIDPattern.MatchString(id) {
			a.dirty[id] = reason
		}
	}
	if len(a.dirty) != 0 {
		a.wakeReloads()
	}
}

// MarkRunnerChanged marks agents that hold an explicit lease for id.
// The caller marks agents that have id as their saved default separately.
func (a *Agents) MarkRunnerChanged(id string) {
	a.mu.Lock()
	resources := maps.Clone(a.resources)
	a.mu.Unlock()
	for agentID, res := range resources {
		if res.envs != nil && res.envs.hasLease(id) {
			a.MarkStale(agentID, "execution environment settings")
		}
	}
}

// MarkRestorable marks an agent if its unavailable default can now open.
func (a *Agents) MarkRestorable(ctx context.Context, agentID string) {
	if envs := a.environments(agentID); envs != nil && envs.restorable(ctx) {
		a.MarkStale(agentID, "machine connected")
	}
}

// wakeReloads signals that dirty agents might now be idle or able to reload.
func (a *Agents) wakeReloads() {
	select {
	case a.reloadWake <- struct{}{}:
	default:
	}
}

func (a *Agents) ReloadWake() <-chan struct{} { return a.reloadWake }

// ReadyReloads returns dirty agents with no tracked operations. ReloadDirty
// checks native work and registers the reload atomically before it opens resources.
func (a *Agents) ReadyReloads() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var ready []string
	if !a.closed && !a.paused {
		for id := range a.dirty {
			if a.busy[id] == 0 {
				ready = append(ready, id)
			}
		}
	}
	return ready
}

func (a *Agents) ReloadDirty(ctx context.Context, agentID string) error {
	return a.reload(ctx, agentID, true)
}
