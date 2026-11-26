package engine

import (
	"context"
	"fmt"

	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
	"github.com/recally-io/dex/internal/runner"
	"github.com/recally-io/dex/internal/vault"
)

// Extension is the native registration descriptor for tools, prompt sections, and hooks.
type Extension = durable.Extension

// ExtensionRuntime provides bot resources before recovery can execute tools.
type ExtensionRuntime struct {
	BotID, BotDir string
	Environment   func(context.Context, int64) (ToolEnvironment, error)
	Secrets       vault.Snapshot
}

type ToolEnvironment struct {
	ID      string
	Vars    map[string]string
	Inherit bool
	Handle  runner.Handle
}

// ExtensionSet installs default extensions before the named built-ins to keep
// tool and prompt order stable. Optional extensions are available by name.
type ExtensionSet struct {
	Before   map[string][]*Extension
	Optional []*Extension
}

func (set ExtensionSet) install(registry harness.Registry, builtins []*Extension) ([]*Extension, error) {
	var defaults []*Extension
	matches := 0
	for _, builtin := range builtins {
		if before, ok := set.Before[builtin.Name]; ok {
			matches++
			defaults = append(defaults, before...)
		}
		defaults = append(defaults, builtin)
	}
	if matches != len(set.Before) {
		return nil, fmt.Errorf("default extension placement refers to an unknown built-in")
	}
	for _, group := range [][]*Extension{defaults, set.Optional} {
		for _, extension := range group {
			if err := registry.Install(extension); err != nil {
				return nil, err
			}
		}
	}
	return defaults, nil
}

// ExtensionEdit selects installed extensions by their persisted names.
type ExtensionEdit struct {
	Add, Remove []string
}

func (edit ExtensionEdit) native() *harness.ExtensionEdit {
	names := func(list []string) []*Extension {
		if len(list) == 0 {
			return nil
		}
		out := make([]*Extension, 0, len(list))
		for _, name := range list {
			out = append(out, &Extension{Name: name})
		}
		return out
	}
	return &harness.ExtensionEdit{Add: names(edit.Add), Remove: names(edit.Remove)}
}
