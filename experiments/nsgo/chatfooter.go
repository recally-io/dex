package main

import (
	"strconv"
	"strings"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

// chatUserFooter leaves the timestamp visible when copy is not shown on a fine pointer.
func chatUserFooter(cx *core.Scope, line func() chatLine, hovered func() bool) core.Node {
	colors := ui.ColorsOf(cx)
	canCopy := func() bool { row := line(); return row.EntryID > 0 && row.Text != "" }
	stamp := func() string {
		row := line()
		origin, _, _ := strings.Cut(row.Origin, ":")
		var site string
		switch origin {
		case "telegram":
			site = "Telegram"
		case "wechat":
			site = "WeChat"
		case "feishu":
			site = "Feishu"
		}
		clock := chatStamp(row.At)
		if site != "" && clock != "" {
			return site + " · " + clock
		}
		return site + clock
	}
	show := func() bool { return cx.Env().Pointer() == core.PointerCoarse || hovered != nil && hovered() }
	visible := core.Style{Opacity: core.Alpha(1)}
	return core.Show{When: func() bool { return line().Role == "user" && (canCopy() || stamp() != "") }, Then: func(*core.Scope) core.Node {
		return core.View{TestID: "chat-user-footer", Style: core.Style{Direction: core.Row, Gap: 2, AlignItems: core.AlignCenter, Height: core.Pt(28)}, Children: []core.Node{
			core.Show{When: canCopy, Then: func(scope *core.Scope) core.Node {
				return core.View{Style: core.Style{Hover: &visible, FocusWithin: &visible},
					StyleFunc: func() core.Style { return chatFooterOpacity(show()) }, Children: []core.Node{
						chatFooterCopy(scope, colors, "chat-copy-message", "Copy message", func() string { return line().Text }),
					}}
			}},
			core.Show{When: func() bool { return stamp() != "" }, Then: func(*core.Scope) core.Node {
				return core.Text{TestID: "chat-user-stamp", ContentFunc: stamp, MaxLines: 1, Style: core.Style{FontSize: 11.5, LineHeight: 17.25,
					Padding: core.Symmetric(0, 6)}, StyleFunc: ui.Colored(colors.Muted)}
			}},
		}}
	}}
}

// chatAssistantFooter occupies 32 points on a coarse pointer. The message view hosts it on a fine pointer.
func chatAssistantFooter(cx *core.Scope, line func() chatLine, hovered func() bool) core.Node {
	colors := ui.ColorsOf(cx)
	visible := core.Style{Opacity: core.Alpha(1)}
	return core.Show{When: func() bool { row := line(); return row.Role == "assistant" && row.Ending }, Then: func(*core.Scope) core.Node {
		return core.View{TestID: "chat-assistant-footer", Style: core.Style{Height: core.Pt(32), Padding: core.Edges{Top: 4},
			Direction: core.Row, AlignItems: core.AlignCenter, Gap: 6, Hover: &visible, FocusWithin: &visible}, StyleFunc: func() core.Style {
			if cx.Env().Pointer() != core.PointerFine {
				return core.Style{}
			}
			style := chatFooterOpacity(hovered != nil && hovered())
			style.Position, style.Top, style.Left, style.Right = core.Absolute, core.Pct(100), core.Pt(0), core.Pt(0)
			return style
		}, Children: []core.Node{
			core.Show{When: func() bool { return line().Model != "" }, Then: func(*core.Scope) core.Node {
				return core.Text{TestID: "chat-answer-model", ContentFunc: func() string { return line().Model }, MaxLines: 1,
					Style: core.Style{FontSize: 11.5, LineHeight: 17.25, FontFamily: "JetBrains Mono Variable, ui-monospace, monospace",
						FlexShrink: 1, MinWidth: core.Pt(0)}, StyleFunc: ui.Colored(colors.Muted)}
			}},
			core.Show{When: func() bool { return line().HasUsage }, Then: func(*core.Scope) core.Node {
				return core.Text{TestID: "chat-answer-usage", ContentFunc: func() string {
					u := line().Usage
					return chatFooterCount(u.Input) + " in · " + chatFooterCount(u.Output) + " out"
				}, MaxLines: 1, Style: core.Style{FontSize: 11.5, LineHeight: 17.25, FontVariant: core.FontTabularNums}, StyleFunc: ui.Colored(colors.Muted)}
			}},
			core.Show{When: func() bool { row := line(); return !row.Live && strings.TrimSpace(row.Text) != "" }, Then: func(scope *core.Scope) core.Node {
				return chatFooterCopy(scope, colors, "chat-copy-answer", "Copy answer", func() string { return line().Text })
			}},
		}}
	}}
}

func chatFooterOpacity(show bool) core.Style {
	if show {
		return core.Style{Opacity: core.Alpha(1)}
	}
	return core.Style{Opacity: core.Alpha(0)}
}

func chatFooterCopy(cx *core.Scope, colors ui.Colors, id, label string, text func() string) core.Node {
	return base.Button{TestID: id, Label: label, OnPress: func(core.PressEvent) { cx.SetClipboard(text()) },
		Style:     core.Style{Width: core.Pt(28), Height: core.Pt(28), FlexShrink: 0, AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter, CornerRadius: 6},
		StyleFunc: func() core.Style { return core.Style{Hover: &core.Style{Background: colors.Hover()}} },
		Children:  []core.Node{ui.Icon(lucide.Copy, 14, colors.Muted)}}
}

func chatFooterCount(n int) string {
	digits := strconv.Itoa(n)
	start := 0
	if digits[0] == '-' {
		start = 1
	}
	for i := len(digits) - 3; i > start; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return digits
}
