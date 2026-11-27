package main

import (
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

func (c *composerContext) projectPane(cx *core.Scope) core.Node {
	const prefix = "composer-project"
	session := core.Untracked(cx, c.session)
	picker := newProjectPicker(cx, c.botID, session, prefix, c.saving, func(saved chatSession) {
		c.config.Set(composerConfigOf(saved))
		c.status.Set("已保存。改动从下一条消息起生效。")
		c.panel.Set("settings")
	})
	return core.View{Style: core.Style{Gap: 8}, Children: []core.Node{
		ui.Button{Text: "返回会话设置", TestID: prefix + "-back", Variant: ui.ButtonGhost,
			Disabled: func() bool { return c.saving.Get() != "" },
			OnPress:  func() { c.status.Set(""); c.panel.Set("settings") }},
		picker.body, picker.saveError, picker.clear, picker.use,
	}}
}
