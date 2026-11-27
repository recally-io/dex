package main

import (
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

const composerInputLimit = 32000

// blocked makes the editor read-only while the current action is pending.
func composerInput(_ *core.Scope, draft, placeholder func() string, blocked func() bool, change func(string), composing func(bool), submit func(), navigate func(string) bool) core.Node {
	return core.Func(func(cx *core.Scope) core.Node {
		var ref core.Ref
		marked := false
		setMarked := func(next bool) {
			if marked == next {
				return
			}
			marked = next
			if composing != nil {
				composing(next)
			}
		}
		cx.Cleanup(func() { setMarked(false) })
		readOnly := func() bool { return blocked != nil && blocked() }
		if navigate != nil {
			composerInputKeys(cx, &ref, func(key string) bool {
				return !marked && !readOnly() && navigate(key)
			})
		}
		behavior := core.SubmitBehaviorSubmit
		if cx.Env().OS() == core.OSIOS {
			// UIKit does not tell OnSubmit if Return came from the software keyboard.
			behavior = core.SubmitBehaviorNewline
		}
		colors := ui.ColorsOf(cx)
		label := func() string {
			if placeholder != nil {
				if text := placeholder(); text != "" {
					return text
				}
			}
			return "Message"
		}
		return ui.Input{
			Ref: &ref, TestID: "draft", LabelFunc: label, Value: draft, PlaceholderFunc: placeholder,
			Multiline: true, MaxLength: composerInputLimit, SubmitBehavior: behavior, ReadonlyFunc: readOnly,
			OnChange: func(text string) {
				if len(text) > composerInputLimit {
					// Some input method commits bypass the platform's insertion filter.
					text = text[:core.UTF16Offset(text, composerInputLimit)]
				}
				change(text)
			},
			OnCompositionChange: func(c core.Composition) { setMarked(c.Composing) },
			OnBlur:              func() { setMarked(false) },
			OnSubmit: func(string) {
				if !marked && !readOnly() && submit != nil {
					submit()
				}
			},
			Style: core.Style{MinHeight: core.Pt(44), MaxHeight: core.Pt(200), Padding: core.Symmetric(6, 4),
				BorderWidth: core.Zero, FontSize: 12, LineHeight: 20},
			StyleFunc: func() core.Style { return core.Style{Background: colors.Surface(), PlaceholderColor: colors.Muted()} },
		}
	})
}
