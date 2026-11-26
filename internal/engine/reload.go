package engine

import (
	"context"
	"maps"
)

// MarkStale requests an idle reload. Bots that have not opened load the current
// configuration on their first open.
func (a *Bots) MarkStale(botID, reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || (a.resources[botID] == nil && a.opening[botID] == nil) {
		return
	}
	a.dirty[botID] = reason
	if a.busy[botID] == 0 {
		a.wakeReloads()
	}
}

func (a *Bots) markExtensionChange(botID, scope, reason string) {
	if scope != "dex" {
		a.MarkStale(botID, reason)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	for id := range a.resources {
		if botIDPattern.MatchString(id) {
			a.dirty[id] = reason
		}
	}
	for id := range a.opening {
		if botIDPattern.MatchString(id) {
			a.dirty[id] = reason
		}
	}
	if len(a.dirty) != 0 {
		a.wakeReloads()
	}
}

// MarkRunnerChanged marks bots that hold an explicit lease for id.
// The caller marks bots that have id as their saved default separately.
func (a *Bots) MarkRunnerChanged(id string) {
	a.mu.Lock()
	resources := maps.Clone(a.resources)
	a.mu.Unlock()
	for botID, res := range resources {
		if res.envs != nil && res.envs.hasLease(id) {
			a.MarkStale(botID, "execution environment settings")
		}
	}
}

// MarkRestorable marks a bot if its unavailable default can now open.
func (a *Bots) MarkRestorable(ctx context.Context, botID string) {
	if envs := a.environments(botID); envs != nil && envs.restorable(ctx) {
		a.MarkStale(botID, "machine connected")
	}
}

// wakeReloads signals that dirty bots might now be idle or able to reload.
func (a *Bots) wakeReloads() {
	select {
	case a.reloadWake <- struct{}{}:
	default:
	}
}

func (a *Bots) ReloadWake() <-chan struct{} { return a.reloadWake }

// ReadyReloads returns dirty bots with no tracked operations. ReloadDirty
// checks native work and registers the reload atomically before it opens resources.
func (a *Bots) ReadyReloads() []string {
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

func (a *Bots) ReloadDirty(ctx context.Context, botID string) error {
	return a.reload(ctx, botID, true)
}
