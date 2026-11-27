package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

func toolsPage(cx *core.Scope, botID string) core.Node {
	return botProfileScreen(cx, botID, "integrations", nil, nil)
}

type toolsFeed[T any] struct {
	cx      *core.Scope
	value   *core.Signal[*T]
	busy    *core.Signal[bool]
	fresh   *core.Signal[bool]
	message *core.Signal[string]
}

func toolsNewFeed[T any](cx *core.Scope) toolsFeed[T] {
	return toolsFeed[T]{cx, core.NewSignal[*T](cx, nil), core.NewSignal(cx, false), core.NewSignal(cx, false), core.NewSignal(cx, "")}
}

func (f toolsFeed[T]) run(wait, success, failure string, load func(context.Context) (T, error)) {
	if f.busy.Get() {
		return
	}
	f.busy.Set(true)
	f.message.Set(wait)
	work(f.cx, load, func(data T, err error) {
		f.cx.Batch(func() {
			if err == nil {
				f.value.Set(new(data))
				f.message.Set(success)
			} else {
				f.message.Set(failure)
			}
			f.fresh.Set(err == nil)
			f.busy.Set(false)
		})
	})
}

func (f toolsFeed[T]) status(id string) core.Node {
	return core.Show{When: func() bool { return f.message.Get() != "" }, Then: func(*core.Scope) core.Node {
		return core.Text{TestID: id, Role: core.RoleStatus, ContentFunc: f.message.Get}
	}}
}

func toolsGet[T any](ctx context.Context, path string) (out T, err error) {
	err = requestJSON(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

func toolsCaption(cx *core.Scope, text string) core.Text {
	return core.Text{Content: text, StyleFunc: func() core.Style {
		return core.Style{FontSize: 12.5, Color: ui.ColorsOf(cx).Muted()}
	}}
}

func toolsLabel(text string) core.Node {
	return core.Text{Content: text, Style: core.Style{FontSize: 13.5, FontWeight: core.FontWeightMedium}}
}

func toolsRow(children ...core.Node) core.Node {
	return core.View{Style: core.Style{Gap: 4, Padding: core.Symmetric(12, 16)}, Children: children}
}

func toolsButton(id, text string, variant ui.ButtonVariant, disabled func() bool, press func()) ui.Button {
	return ui.Button{TestID: id, Text: text, Variant: variant, Disabled: disabled, OnPress: press,
		Style: core.Style{Height: core.Pt(30), AlignSelf: core.AlignStart}}
}

type toolsCatalog struct {
	Skills        []toolsSkill  `json:"skills"`
	Servers       []toolsServer `json:"servers"`
	Warnings      []string      `json:"warnings"`
	Errors        []string      `json:"errors"`
	ReloadPending bool          `json:"reloadPending"`
}

type toolsSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Scope       string `json:"scope"`
	Overrides   bool   `json:"overrides"`
	Error       string `json:"error"`
}

type toolsServer struct {
	Name      string   `json:"name"`
	Scope     string   `json:"scope"`
	State     string   `json:"state"`
	Tools     []string `json:"tools"`
	OAuth     string   `json:"oauth"`
	Overrides bool     `json:"overrides"`
}

func toolsCatalogSection(cx *core.Scope, path, section string) core.Node {
	feed := toolsNewFeed[toolsCatalog](cx)
	refresh := func() {
		feed.run("Loading integrations…", "", "Could not load integrations.", func(ctx context.Context) (toolsCatalog, error) {
			return toolsGet[toolsCatalog](ctx, path)
		})
	}
	reload := func() {
		feed.run("Reloading…", "Integrations reloaded.", "Could not reload tools and MCP servers. Try again when this Bot is idle.", func(ctx context.Context) (out toolsCatalog, err error) {
			err = requestJSON(ctx, http.MethodPost, path+"/reload", nil, &out)
			return out, err
		})
	}
	refresh()
	return core.View{Style: core.Style{Gap: 24}, Children: []core.Node{
		feed.status("tools-status"),
		core.Show{When: func() bool { return !feed.busy.Get() && !feed.fresh.Get() }, Then: func(*core.Scope) core.Node {
			return toolsButton("tools-refresh", "Try again", ui.ButtonOutline, nil, refresh)
		}},
		core.Dynamic(func(cx *core.Scope) core.Node {
			catalog := feed.value.Get()
			if catalog == nil {
				return core.Group{}
			}
			var notes, skills, servers []core.Node
			if len(catalog.Errors) > 0 {
				notes = append(notes, toolsRow(toolsLabel("Dex cannot read some files"), toolsCaption(cx, strings.Join(catalog.Errors, "\n"))))
			}
			if len(catalog.Warnings) > 0 {
				notes = append(notes, toolsRow(toolsCaption(cx, strings.Join(catalog.Warnings, "\n"))))
			}
			if catalog.ReloadPending && len(catalog.Errors) == 0 {
				notes = append(notes, toolsRow(toolsCaption(cx, "Change waiting for this bot to be idle")))
			}
			for _, skill := range catalog.Skills {
				var detail core.Node = core.Text{Content: skill.Description}
				if skill.Error != "" {
					detail = toolsCaption(cx, skill.Error)
				}
				skills = append(skills, toolsRow(toolsLabel(skill.Name), toolsCaption(cx, toolsScopeLabel(skill.Scope, skill.Overrides)), detail))
			}
			if len(skills) == 0 {
				skills = append(skills, toolsRow(toolsCaption(cx, "No skills available to this Bot.")))
			}
			for _, server := range catalog.Servers {
				details := toolsScopeLabel(server.Scope, server.Overrides) + " · " + toolsMCPState(server.State)
				switch server.OAuth {
				case "authorize":
					details += " · Authorization required"
				case "reauthorize":
					details += " · Reauthorization required"
				case "authorized":
					details += " · Authorized"
				}
				row := []core.Node{toolsLabel(server.Name), toolsCaption(cx, details)}
				if len(server.Tools) != 0 {
					row = append(row, toolsCaption(cx, fmt.Sprintf("Tools (%d): %s", len(server.Tools), strings.Join(server.Tools, ", "))))
				}
				servers = append(servers, toolsRow(row...))
			}
			if len(servers) == 0 {
				servers = append(servers, toolsRow(toolsCaption(cx, "No MCP servers available to this Bot.")))
			}
			sections := core.Group{}
			if len(notes) != 0 {
				sections = append(sections, botProfileSection(cx, "Configuration", "", botProfilePanel(cx, notes...)))
			}
			var skillsHeading, mcpHeading *core.Ref
			if section == "skills" {
				skillsHeading = &core.Ref{}
				cx.Post(func() { skillsHeading.Reveal(core.RevealOptions{}) })
			} else if section == "mcp" {
				mcpHeading = &core.Ref{}
				cx.Post(func() { mcpHeading.Reveal(core.RevealOptions{}) })
			}
			sections = append(sections, botProfileSectionAt(cx, skillsHeading, "Skills", "", botProfilePanel(cx, skills...)),
				botProfileSectionAt(cx, mcpHeading, "MCP servers", "Reload refreshes this Bot's tools and MCP connections when it is idle.",
					toolsButton("tools-reload", "Reload", ui.ButtonSecondary, feed.busy.Get, reload), botProfilePanel(cx, servers...)))
			return sections
		}),
		toolsCaption(cx, "Use Dex Web to add skills or MCP servers, or to authorize an MCP connection."),
	}}
}

func toolsScopeLabel(scope string, overrides bool) string {
	label := scope
	if scope == "dex" {
		label = "Shared"
	}
	if overrides {
		label += ", replaces a wider entry"
	}
	return label
}

func toolsMCPState(state string) string {
	switch state {
	case "idle":
		return "Idle"
	case "connected":
		return "Connected"
	case "failed":
		return "Failed"
	case "disabled":
		return "Disabled"
	default:
		return "Unknown"
	}
}

type toolsChannel struct {
	Enabled bool `json:"enabled"`
	Status  struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	} `json:"status"`
}

type toolsChannels struct {
	Telegram toolsChannel `json:"telegram"`
	Wechat   toolsChannel `json:"wechat"`
	Feishu   struct {
		Enabled bool   `json:"enabled"`
		Paired  bool   `json:"paired"`
		Status  string `json:"status"`
	} `json:"feishu"`
}

type toolsChannelInfo struct {
	key, name, description string
	icon                   func() *core.SVG
}

var toolsChannelList = []toolsChannelInfo{
	{"telegram", "Telegram", "Private messages from allowed people.", lucide.Send},
	{"wechat", "WeChat", "Private messages from the paired owner.", lucide.MessageCircle},
	{"feishu", "Feishu / Lark", "Private messages from the paired owner.", lucide.Feather},
}

func toolsChannelStatus(channels toolsChannels, key string) (enabled bool, state, reason string) {
	switch key {
	case "telegram":
		return channels.Telegram.Enabled, channels.Telegram.Status.State, channels.Telegram.Status.Reason
	case "wechat":
		return channels.Wechat.Enabled, channels.Wechat.Status.State, channels.Wechat.Status.Reason
	case "feishu":
		return channels.Feishu.Enabled, channels.Feishu.Status, ""
	default:
		return false, "", ""
	}
}

func toolsLoadChannels(cx *core.Scope, path string) (toolsFeed[toolsChannels], func()) {
	feed := toolsNewFeed[toolsChannels](cx)
	refresh := func() {
		feed.run("Loading channels…", "", "Could not load channels.", func(ctx context.Context) (toolsChannels, error) {
			return toolsGet[toolsChannels](ctx, path)
		})
	}
	refresh()
	return feed, refresh
}

func toolsChannelsSection(cx *core.Scope, base string, navigate func(string)) core.Node {
	feed, refresh := toolsLoadChannels(cx, base+"/channels")
	targets := toolsNotificationTargets(cx, base+"/notification-targets")
	return core.View{Style: core.Style{Gap: 16}, Children: []core.Node{
		botProfileRetry(feed, "tools-channels-refresh", refresh),
		core.Dynamic(func(cx *core.Scope) core.Node {
			channels := feed.value.Get()
			if channels == nil {
				return core.Group{}
			}
			var rows []core.Node
			for _, channel := range toolsChannelList {
				_, state, _ := toolsChannelStatus(*channels, channel.key)
				badge := toolsCaption(cx, toolsChannelState(channel.key, state))
				badge.TestID = "tools-" + channel.key + "-status"
				var name core.Node = toolsLabel(channel.name)
				if navigate != nil {
					name = ui.Button{TestID: "tools-channel-" + channel.key, Text: channel.name,
						Variant: ui.ButtonGhost, Icon: channel.icon, Style: core.Style{AlignSelf: core.AlignStart, Height: core.Pt(32)},
						OnPress: func() { navigate("channels/" + channel.key) }}
				}
				rows = append(rows, core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 12, Padding: core.Symmetric(10, 16)}, Children: []core.Node{
					core.View{Style: core.Style{Gap: 2, FlexGrow: 1, FlexShrink: 1}, Children: []core.Node{name, toolsCaption(cx, channel.description)}}, badge,
				}})
			}
			return botProfileSection(cx, "Channels", "Each channel talks to the same bot.", botProfilePanel(cx, append(rows, targets)...))
		}),
	}}
}

func toolsChannelDetails(cx *core.Scope, path string, channel toolsChannelInfo) core.Node {
	feed, refresh := toolsLoadChannels(cx, path)
	confirm := core.NewSignal(cx, false)
	canDisconnect := func() bool { return feed.fresh.Get() && !feed.busy.Get() }
	disconnect := func() {
		if !canDisconnect() || !confirm.Get() {
			return
		}
		confirm.Set(false)
		feed.run("Turning off…", channel.name+" is off.", "Could not confirm the change. Reload channels before you try again.", func(ctx context.Context) (toolsChannels, error) {
			if err := requestJSON(ctx, http.MethodDelete, path+"/"+channel.key, nil, nil); err != nil {
				return toolsChannels{}, err
			}
			return toolsGet[toolsChannels](ctx, path)
		})
	}
	return core.View{Style: core.Style{Gap: 24}, Children: []core.Node{
		feed.status("tools-channels-status"),
		core.Show{When: func() bool { return !feed.busy.Get() && !feed.fresh.Get() }, Then: func(*core.Scope) core.Node {
			return toolsButton("tools-channels-refresh", "Try again", ui.ButtonOutline, nil, refresh)
		}},
		core.Dynamic(func(cx *core.Scope) core.Node {
			channels := feed.value.Get()
			if channels == nil {
				return core.Group{}
			}
			enabled, state, reason := toolsChannelStatus(*channels, channel.key)
			badge := toolsCaption(cx, toolsChannelState(channel.key, state))
			badge.TestID = "tools-" + channel.key + "-status"
			rows := []core.Node{badge, toolsCaption(cx, "Use Dex Web to connect, edit, or pair this channel.")}
			if reason != "" {
				rows = append(rows, toolsCaption(cx, reason))
			}
			sections := core.Group{botProfileSection(cx, "Connection", "", botProfilePanel(cx, toolsRow(rows...)))}
			if enabled {
				sections = append(sections, botProfileSection(cx, "Turn off", "", core.Show{When: confirm.Get,
					Then: func(cx *core.Scope) core.Node {
						return botProfilePanel(cx, toolsRow(toolsLabel("Turn off "+channel.name+"?"),
							toolsCaption(cx, "Dex forgets the saved credentials. The main timeline stays."),
							toolsButton("tools-"+channel.key+"-confirm", "Turn off", ui.ButtonDanger, func() bool { return !canDisconnect() }, disconnect),
							toolsButton("tools-"+channel.key+"-cancel", "Cancel", ui.ButtonGhost, nil, func() { confirm.Set(false) })))
					}, Else: func(*core.Scope) core.Node {
						return toolsButton("tools-"+channel.key+"-disconnect", "Turn off "+channel.name, ui.ButtonDanger,
							func() bool { return !canDisconnect() }, func() { confirm.Set(true) })
					}}))
			}
			return sections
		}),
	}}
}

func toolsChannelState(key, state string) string {
	switch state {
	case "running":
		return "Connected"
	case "off":
		return "Off"
	case "error":
		return "Error"
	case "needs_login":
		if key == "feishu" {
			return "Needs owner"
		}
		return "Scan again"
	default:
		return "Unknown"
	}
}

type toolsTargets struct {
	PrimaryID string `json:"primaryId"`
	Targets   []struct {
		ID       string `json:"id"`
		Platform string `json:"platform"`
		Peer     string `json:"peer"`
	} `json:"targets"`
}

func toolsNotificationTargets(cx *core.Scope, path string) core.Node {
	feed := toolsNewFeed[toolsTargets](cx)
	get := func(ctx context.Context) (toolsTargets, error) { return toolsGet[toolsTargets](ctx, path) }
	refresh := func() { feed.run("Loading notification targets…", "", "Could not load notification targets.", get) }
	refresh()
	return toolsRow(toolsLabel("Primary notification channel"),
		toolsCaption(cx, "Automations can follow this default or choose another authorized recipient."),
		ui.Select[string]{TestID: "tools-notification-primary", Label: "Primary notification channel",
			Value: func() string {
				if saved := feed.value.Get(); saved != nil {
					return saved.PrimaryID
				}
				return ""
			},
			OptionsFunc: func() []ui.Option[string] {
				options := []ui.Option[string]{{Label: "None"}}
				if saved := feed.value.Get(); saved != nil {
					for _, target := range saved.Targets {
						options = append(options, ui.Option[string]{Value: target.ID, Label: target.Platform + ": " + target.Peer})
					}
				}
				return options
			}, Disabled: func() bool { return !feed.fresh.Get() || feed.busy.Get() },
			OnChange: func(id string) {
				if !feed.fresh.Get() || feed.busy.Get() {
					return
				}
				feed.run("Saving…", "Primary notification channel updated.", "Could not confirm the change to notification targets.", func(ctx context.Context) (toolsTargets, error) {
					if err := requestJSON(ctx, http.MethodPut, path, map[string]string{"targetId": id}, nil); err != nil {
						return toolsTargets{}, err
					}
					return get(ctx)
				})
			}},
		feed.status("tools-notification-status"),
		core.Show{When: func() bool { return !feed.busy.Get() && !feed.fresh.Get() }, Then: func(*core.Scope) core.Node {
			return toolsButton("tools-notification-refresh", "Try again", ui.ButtonOutline, nil, refresh)
		}},
	)
}
