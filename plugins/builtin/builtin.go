// Package builtin lists the plugins that ship with Dex.
package builtin

import (
	"github.com/recally-io/dex/internal/plugin"
	"github.com/recally-io/dex/plugins/email"
	"github.com/recally-io/dex/plugins/github"
	"github.com/recally-io/dex/plugins/webhook"
)

// Factories returns the built-in plugin factories in registration order.
func Factories() []plugin.Factory {
	return []plugin.Factory{github.New, webhook.New, email.New}
}
