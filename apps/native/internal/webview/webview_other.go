//go:build !darwin && !android

package webview

import (
	"errors"

	"github.com/nsgo-dev/nsgo"
	"github.com/nsgo-dev/nsgo/core"
)

// Options registers no workspace view on unsupported platforms.
func Options() []nsgo.Option { return nil }

// ClearSiteData reports that there is no workspace data store on this platform.
func ClearSiteData(_ *core.Scope, _ string, done func(error)) {
	done(errors.New("workspace web view is not available on this platform"))
}
