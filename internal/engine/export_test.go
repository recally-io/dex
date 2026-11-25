package engine

import "github.com/recally-io/dex/internal/vault"

func BuiltInSchemaExtensions() []*Extension {
	return []*Extension{toolSearchExtension(), codingTools(nil, 0, vault.Snapshot{}), (&memoryRepo{}).Extension("bot", nil)}
}
