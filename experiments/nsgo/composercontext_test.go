package main

import (
	"errors"
	"strings"
	"testing"
)

func TestUserCanSendWithFallbackButNotWithIgnoredBotPrimary(t *testing.T) {
	t.Parallel()

	// Given a Bot primary in the catalog and an explicit unavailable session override.
	state := composerCatalog{
		bot:     botSettingsValues{Provider: "p", ModelID: "primary"},
		models:  []botSettingsCatalogItem{{Provider: "p", ID: "primary"}},
		runners: []composerRunner{{ID: "local", Default: true, Status: "available"}},
	}
	own := composerSessionSettings{Provider: "p", ModelID: "removed"}

	// When the override is active, the Bot primary is not a substitute.
	if got := state.reason(own, ""); !strings.Contains(got, "模型") {
		t.Fatalf("unavailable override did not block sending: %q", got)
	}

	// Then the actual Bot fallback is allowed, even when the override is unavailable.
	state.bot.FallbackProvider, state.bot.FallbackModelID = "p", "fallback"
	state.models = append(state.models, botSettingsCatalogItem{Provider: "p", ID: "fallback"})
	for _, settings := range []composerSessionSettings{own, {}} {
		if got := state.reason(settings, ""); got != "" {
			t.Fatalf("available model and Runner blocked sending: %q", got)
		}
	}
	state.models = state.models[1:]
	if got := state.reason(composerSessionSettings{}, ""); got != "" {
		t.Fatalf("fallback-only path blocked sending: %q", got)
	}
}

func TestUserCannotSendToOfflineOrUnconfirmedRunner(t *testing.T) {
	t.Parallel()

	// Given an available model, an offline default Runner and an available Local Runner.
	state := composerCatalog{
		bot:    botSettingsValues{Provider: "p", ModelID: "model"},
		models: []botSettingsCatalogItem{{Provider: "p", ID: "model"}},
		runners: []composerRunner{
			{ID: "remote", Name: "工作机", Default: true, Status: "offline"},
			{ID: "local", Status: "available"},
		},
	}

	// When the session follows the Bot or references a removed Runner, sending is blocked.
	if got := state.reason(composerSessionSettings{}, ""); !strings.Contains(got, "离线") {
		t.Fatalf("offline default did not block sending: %q", got)
	}
	if got := state.reason(composerSessionSettings{}, "removed"); !strings.Contains(got, "不在") {
		t.Fatalf("removed Runner did not block sending: %q", got)
	}

	// Then Local works only while its status is known and the model catalog is verified.
	if got := state.reason(composerSessionSettings{}, "local"); got != "" {
		t.Fatalf("available explicit Runner blocked sending: %q", got)
	}
	state.runnerErr = errors.New("read failed")
	if got := state.reason(composerSessionSettings{}, "local"); !strings.Contains(got, "Runner") {
		t.Fatalf("unknown Runner status did not block sending: %q", got)
	}
	state.modelErr = errors.New("catalog failed")
	if got := state.reason(composerSessionSettings{}, "local"); !strings.Contains(got, "模型") {
		t.Fatalf("unknown model availability did not block sending: %q", got)
	}
}

func TestUserCanExplicitlyChooseLocalWhenBotRunnerWasRemoved(t *testing.T) {
	t.Parallel()

	// Given a deleted Bot default, Dex lists its unavailable default and Local separately.
	state := composerCatalog{
		bot:    botSettingsValues{Provider: "p", ModelID: "model"},
		models: []botSettingsCatalogItem{{Provider: "p", ID: "model"}},
		runners: []composerRunner{
			{ID: "local", Default: true, Status: "unavailable: runner not found"},
			{ID: "local", Status: "available"},
		},
	}

	// Then sending to the default is unsafe, but an explicit Local selection works.
	if got := state.reason(composerSessionSettings{}, ""); !strings.Contains(got, "不可用") {
		t.Fatalf("invalid Bot default did not block sending: %q", got)
	}
	if got := state.reason(composerSessionSettings{}, "local"); got != "" {
		t.Fatalf("the explicit Local Runner was blocked: %q", got)
	}
}
