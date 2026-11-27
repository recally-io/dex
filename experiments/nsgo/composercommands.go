package main

import (
	"context"
	"net/http"
	"strings"
	"unicode"
	"uuid"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

type composerCommand struct {
	name, alias, title, help string
}

var composerCommandList = []composerCommand{
	{"clear", "new", "/clear [交接内容] · /new", "清除当前上下文。可保留一段交接内容。"},
	{"compact", "", "/compact [保留内容]", "将较早的消息压缩为摘要，释放上下文空间。"},
}

type composerCommandDraft struct {
	name, arg string
	typing    bool
}

func composerCommandParse(text string) (composerCommandDraft, bool) {
	rest, ok := strings.CutPrefix(text, "/")
	if !ok {
		return composerCommandDraft{}, false
	}
	if i := strings.IndexFunc(rest, unicode.IsSpace); i >= 0 {
		return composerCommandDraft{name: strings.ToLower(rest[:i]), arg: strings.TrimSpace(rest[i:])}, true
	}
	return composerCommandDraft{name: strings.ToLower(rest), typing: true}, true
}

type composerCommandRetry struct {
	session int
	arg, id string
}

type composerCommands struct {
	cx                 *core.Scope
	botID              string
	session            func() chatSession
	draft              func() string
	setDraft           func(string)
	running, empty     func() bool
	blocked            func() bool
	pending, dismissed *core.Signal[bool]
	selected           *core.Signal[int]
	hint               *core.Signal[string]
	retries            map[string]composerCommandRetry
}

func newComposerCommands(cx *core.Scope, botID string, session func() chatSession, draft func() string, setDraft func(string), running, empty, blocked func() bool) *composerCommands {
	c := &composerCommands{
		cx: cx, botID: botID, session: session, draft: draft, setDraft: setDraft, running: running, empty: empty, blocked: blocked,
		pending: core.NewSignal(cx, false), dismissed: core.NewSignal(cx, false), selected: core.NewSignal(cx, 0),
		hint: core.NewSignal(cx, ""), retries: make(map[string]composerCommandRetry),
	}
	cx.Effect(func() {
		_ = draft()
		c.dismissed.Set(false)
		c.selected.Set(0)
		c.hint.Set("")
	})
	return c
}

func (c *composerCommands) Active() bool {
	_, ok := composerCommandParse(c.draft())
	return ok
}

func (c *composerCommands) Pending() bool { return c.pending.Get() }

func (c *composerCommands) options() []composerCommand {
	parsed, ok := composerCommandParse(c.draft())
	if !ok || !parsed.typing || c.dismissed.Get() || c.Pending() {
		return nil
	}
	var options []composerCommand
	for _, command := range composerCommandList {
		if strings.HasPrefix(command.name, parsed.name) || command.alias != "" && strings.HasPrefix(command.alias, parsed.name) {
			options = append(options, command)
		}
	}
	return options
}

func (c *composerCommands) complete(command composerCommand) {
	c.setDraft("/" + command.name + " ")
}

func (c *composerCommands) Navigate(key string) bool {
	options := c.options()
	if len(options) == 0 {
		return false
	}
	selected := min(c.selected.Get(), len(options)-1)
	switch strings.ToLower(key) {
	case "down", "arrowdown":
		c.selected.Set((selected + 1) % len(options))
	case "up", "arrowup":
		c.selected.Set((selected + len(options) - 1) % len(options))
	case "escape", "esc":
		c.dismissed.Set(true)
	case "tab":
		c.complete(options[selected])
	default:
		return false
	}
	return true
}

func composerCommandUnavailable(command string, session chatSession, running, empty, blocked bool) string {
	switch {
	case session.ID == 0:
		return "会话尚未就绪。"
	case !session.writable():
		return "此会话为只读。"
	case blocked:
		return "暂时无法执行，请稍后重试。"
	case command == "clear" && running:
		return "会话正在运行。请等待回复结束，或先停止运行。"
	case command == "clear" && empty:
		return "上下文已经为空。"
	case command == "compact" && empty:
		return "没有可压缩的上下文。"
	default:
		return ""
	}
}

func (c *composerCommands) Submit() {
	raw := c.draft()
	parsed, ok := composerCommandParse(raw)
	if !ok || c.Pending() {
		return
	}
	var command string
	for _, option := range composerCommandList {
		if parsed.name == option.name || option.alias != "" && parsed.name == option.alias {
			command = option.name
			break
		}
	}
	if command == "" {
		if options := c.options(); len(options) != 0 {
			c.complete(options[min(c.selected.Get(), len(options)-1)])
			return
		}
		c.hint.Set("未知命令 /" + parsed.name + "。输入 / 查看命令；如需发送以 / 开头的文本，请在前面加空格。")
		return
	}
	session := c.session()
	if reason := composerCommandUnavailable(command, session, c.running(), c.empty(), c.blocked()); reason != "" {
		c.hint.Set("/" + command + "：" + reason)
		return
	}

	attempt := c.retries[command]
	if attempt.id == "" || attempt.session != session.ID || attempt.arg != parsed.arg {
		attempt = composerCommandRetry{session.ID, parsed.arg, uuid.New().String()}
		c.retries[command] = attempt
	}
	body := struct {
		ID           string `json:"id"`
		Handoff      string `json:"handoff,omitempty"`
		Instructions string `json:"instructions,omitempty"`
	}{ID: attempt.id}
	if command == "clear" {
		body.Handoff = parsed.arg
	} else {
		body.Instructions = parsed.arg
	}
	path := chatSessionPath(c.botID, session.ID) + "/" + command
	c.pending.Set(true)
	c.hint.Set("")
	work(c.cx, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, requestJSON(ctx, http.MethodPost, path, body, nil)
	}, func(_ struct{}, err error) {
		c.pending.Set(false)
		if err == nil {
			delete(c.retries, command)
		}
		if c.session().ID != session.ID || c.draft() != raw {
			return
		}
		if err != nil {
			c.hint.Set("/" + command + " 执行失败，已保留草稿：" + err.Error())
			return
		}
		c.setDraft("")
	})
}

func (c *composerCommands) Panel() core.Node {
	colors := ui.ColorsOf(c.cx)
	return core.View{Style: core.Style{Gap: 4}, Children: []core.Node{
		core.Show{When: func() bool { return len(c.options()) != 0 }, Then: func(*core.Scope) core.Node {
			return core.View{TestID: "composer-commands-list", Role: core.RoleListbox, Label: "命令补全", Style: core.Style{Padding: core.All(4), Gap: 2, BorderWidth: 1, CornerRadius: 8},
				StyleFunc: func() core.Style { return core.Style{Background: colors.Surface(), BorderColor: colors.Border()} }, Children: []core.Node{
					core.Each(c.options, func(option composerCommand) string { return option.name }, func(_ *core.Scope, item func() composerCommand, index func() int) core.Node {
						option := item()
						return base.Button{TestID: "composer-command-" + option.name, Role: core.RoleOption, NoFocus: true, Label: option.title,
							Selected: func() bool { return index() == c.selected.Get() }, OnPress: func(core.PressEvent) { c.complete(option) },
							Style: core.Style{Padding: core.Symmetric(6, 8), Gap: 2, CornerRadius: 6},
							StyleFunc: func() core.Style {
								return core.Style{Selected: &core.Style{Background: colors.Hover()}, Hover: &core.Style{Background: colors.Hover()}}
							}, Children: []core.Node{
								core.Text{Content: option.title, Style: core.Style{FontSize: 13, FontWeight: ui.Medium}, StyleFunc: ui.Colored(colors.Text)},
								core.Text{ContentFunc: func() string {
									if reason := composerCommandUnavailable(option.name, c.session(), c.running(), c.empty(), c.blocked()); reason != "" {
										return reason
									}
									return option.help
								}, Style: core.Style{FontSize: 12}, StyleFunc: ui.Colored(colors.Muted)},
							}}
					}),
				}}
		}},
		core.Show{When: func() bool { return c.hint.Get() != "" || c.Pending() }, Then: func(*core.Scope) core.Node {
			return core.Text{TestID: "composer-command-hint", Role: core.RoleStatus, ContentFunc: func() string {
				if c.Pending() {
					return "正在提交命令…"
				}
				return c.hint.Get()
			}, Style: core.Style{FontSize: 12}, StyleFunc: ui.Colored(colors.Muted)}
		}},
	}}
}
