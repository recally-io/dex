package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"uuid"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

const integrationsInvalidTarget = ":unavailable"

type integrationsTargets struct {
	chosen  *core.Signal[string]
	bots    *core.Signal[[]integrationsTarget]
	issue   *core.Signal[string]
	loading *core.Signal[bool]
	loaded  *core.Signal[bool]
	load    func()
}

func integrationsNewTargets(cx *core.Scope, q integrationsQuery) *integrationsTargets {
	initial := q.target
	if !q.missing && initial == "" {
		initial = integrationsInvalidTarget
	}
	t := &integrationsTargets{chosen: core.NewSignal(cx, initial), bots: core.NewSignalFunc(cx, []integrationsTarget(nil), slices.Equal),
		issue: core.NewSignal(cx, ""), loading: core.NewSignal(cx, false), loaded: core.NewSignal(cx, false)}
	t.load = func() {
		if t.loading.Get() {
			return
		}
		t.loading.Set(true)
		t.issue.Set("")
		work(cx, integrationsRoster, func(bots []integrationsTarget, err error) {
			t.loading.Set(false)
			t.loaded.Set(err == nil)
			if err != nil {
				t.issue.Set(err.Error())
				return
			}
			t.bots.Set(bots)
		})
	}
	cx.Untrack(t.load)
	return t
}

func (t *integrationsTargets) selected() (integrationsTarget, bool) {
	for _, b := range t.bots.Get() {
		if b.ID == t.chosen.Get() {
			return b, true
		}
	}
	return integrationsTarget{}, false
}

func (t *integrationsTargets) validShared() bool {
	if t.chosen.Get() == "" {
		return true
	}
	_, valid := t.selected()
	return valid && t.loaded.Get() && !t.loading.Get()
}

func (t *integrationsTargets) field(cx *core.Scope, label string, shared bool, locked func() bool) core.Node {
	return core.View{Style: core.Style{Gap: 6}, Children: []core.Node{
		integrationsLabel(label),
		ui.Select[string]{Label: label, TestID: "integrations-target", Placeholder: "Choose a Bot", Value: t.chosen.Get, OnChange: t.chosen.Set, Disabled: locked,
			Style: core.Style{Width: core.Pct(100), Height: core.Pt(36)}, OptionsFunc: func() []ui.Option[string] {
				var choices []ui.Option[string]
				selected := t.chosen.Get()
				if _, exists := t.selected(); selected != "" && !exists {
					name := "Unavailable Bot"
					if t.loading.Get() {
						name = "Checking target…"
					}
					choices = append(choices, ui.Option[string]{Value: selected, Label: name, Disabled: func() bool { return true }})
				}
				if shared {
					choices = append(choices, ui.Option[string]{Value: "", Label: "Shared (all Bots)"})
				}
				for _, b := range t.bots.Get() {
					if b.ID != "" {
						choices = append(choices, ui.Option[string]{Value: b.ID, Label: b.Name})
					}
				}
				return choices
			}},
		core.Show{When: func() bool { _, ok := t.selected(); return t.chosen.Get() != "" && t.loaded.Get() && !ok }, Then: func(cx *core.Scope) core.Node {
			return integrationsStatus(cx, "This Bot is not available. Choose a target.")
		}},
		core.Show{When: func() bool { return t.issue.Get() != "" }, Then: func(cx *core.Scope) core.Node {
			return integrationsFailure(cx, "integrations-bots-error", "Could not load Bots", t.issue.Get, t.load)
		}},
	}}
}

func integrationsDialog(cx *core.Scope, id string, title func() string, close func(), locked func() bool, body core.Node) core.Node {
	colors := ui.ColorsOf(cx)
	return base.Dialog{TestID: id, LabelFunc: title, DefaultOpen: true, Animation: core.ModalNone,
		NoCloseOnEscape: true, NoCloseOnBackdropPress: true, NoCloseOnSwipe: true, OnCancel: close,
		BackdropStyle: core.Style{Background: ui.Black(.10), BackdropFilters: []core.Filter{core.Blur(4)}},
		PopupStyle:    core.Style{Width: core.Pt(540), BorderWidth: 1, CornerRadius: 12, Overflow: core.Hidden},
		PopupStyleFunc: func() core.Style {
			w := cx.Env().Window()
			kb := cx.Env().KeyboardHeight()
			insets := core.Insets(cx)
			return core.Style{MaxWidth: core.Pt(max(0, w.Width-32-insets.Left-insets.Right)), MaxHeight: core.Pt(max(140, w.Height-max(kb, insets.Bottom)-insets.Top-32)),
				Margin: core.Edges{Bottom: kb}, Background: colors.Surface(), BorderColor: colors.Border()}
		},
		Children: []core.Node{core.Scroll{Style: core.Style{FlexShrink: 1, Padding: core.All(18)}, Children: []core.Node{
			core.View{Style: core.Style{Gap: 14}, Children: []core.Node{
				core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, JustifyContent: core.JustifySpaceBetween, Gap: 8}, Children: []core.Node{
					core.Text{ContentFunc: title, Role: core.RoleHeader, Style: core.Style{FontSize: 18, FontWeight: ui.Semibold, FlexShrink: 1}},
					ui.Button{Icon: lucide.X, Label: "Close", Variant: ui.ButtonGhost, Side: 30, IconSize: 16, Disabled: locked, OnPress: close},
				}}, body,
			}},
		}}},
	}
}

func integrationsInput(cx *core.Scope, id, label, hint string, value *core.Signal[string], locked func() bool) core.Node {
	items := []core.Node{integrationsLabel(label), ui.Input{TestID: id, Label: label, Value: value.Get, OnChange: value.Set, ReadonlyFunc: locked,
		NoAutoCorrect: true, AutoCapitalize: core.AutoCapitalizeNone, AutoComplete: core.AutoCompleteOff, Style: core.Style{Height: core.Pt(36)}}}
	if hint != "" {
		items = append(items, integrationsMuted(cx, hint))
	}
	return core.View{Style: core.Style{Gap: 5}, Children: items}
}

func integrationsStack(items ...core.Node) core.Node {
	return core.View{Style: core.Style{Gap: 14}, Children: items}
}
func integrationsActions(items ...core.Node) core.Node {
	return core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, JustifyContent: core.JustifyEnd, Gap: 8}, Children: items}
}

func integrationsAddDialog(cx *core.Scope, q integrationsQuery, initial integrationsMCPDraft, close, refresh func()) core.Node {
	target := integrationsNewTargets(cx, q)
	name, address := core.NewSignal(cx, initial.Name), core.NewSignal(cx, initial.URL)
	auth := core.NewSignal(cx, initial.Auth)
	client, key, scope := core.NewSignal(cx, initial.Client), core.NewSignal(cx, initial.Secret), core.NewSignal(cx, initial.Scope)
	headers := core.NewSignalFunc(cx, slices.Clone(initial.Headers), slices.Equal)
	busy, advanced, attempted := core.NewSignal(cx, false), core.NewSignal(cx, false), core.NewSignal(cx, false)
	issue := core.NewSignal(cx, "")
	var saved core.Node
	added := core.NewSignal(cx, false)
	addedName := core.NewSignal(cx, "")
	isGoogle := initial.Auth == "oauth-client"
	read := func() integrationsMCPDraft {
		return integrationsMCPDraft{Name: name.Get(), URL: address.Get(), Auth: auth.Get(), Client: client.Get(), Secret: key.Get(), Scope: scope.Get(), Headers: headers.Get()}
	}
	closeSafe := func() {
		if !busy.Get() {
			close()
		}
	}
	formTitle := func() string {
		if added.Get() {
			return "Added " + addedName.Get()
		}
		return "Add MCP server"
	}
	validation := func() string {
		if _, _, err := integrationsMCPRequest(target.chosen.Get(), read()); err != nil {
			return err.Error()
		}
		return ""
	}
	save := func() {
		if busy.Get() || !target.validShared() {
			return
		}
		attempted.Set(true)
		active := read()
		path, body, err := integrationsMCPRequest(target.chosen.Get(), active)
		if err != nil {
			return
		}
		bot, _ := target.selected()
		busy.Set(true)
		issue.Set("")
		work(cx, func(ctx context.Context) (struct{}, error) {
			return struct{}{}, requestJSON(ctx, http.MethodPost, path, body, nil)
		}, func(_ struct{}, err error) {
			busy.Set(false)
			if err != nil {
				issue.Set(err.Error())
				return
			}
			addedName.Set(body.Name)
			saved = core.Func(func(cx *core.Scope) core.Node { return integrationsSaved(cx, body.Name, bot, closeSafe) })
			added.Set(true)
			if bot.ID == "" {
				refresh()
			}
		})
	}
	body := core.Dynamic(func(cx *core.Scope) core.Node {
		if added.Get() {
			return saved
		}
		authFields := core.Dynamic(func(cx *core.Scope) core.Node {
			switch auth.Get() {
			case "header":
				return integrationsHeaderFields(cx, headers, busy.Get)
			case "oauth":
				return integrationsStack(
					integrationsMuted(cx, "You sign in to the server in this browser after you add it. Most servers need nothing else."),
					ui.Button{Text: "Advanced options", TestID: "integrations-oauth-advanced", Variant: ui.ButtonGhost, Disabled: busy.Get, Style: core.Style{AlignSelf: core.AlignStart}, OnPress: func() { advanced.Set(!advanced.Get()) }},
					core.Show{When: advanced.Get, Then: func(cx *core.Scope) core.Node { return integrationsOAuthFields(cx, client, key, scope, busy.Get) }},
				)
			case "oauth-client":
				return integrationsOAuthFields(cx, client, key, scope, busy.Get)
			default:
				return integrationsMuted(cx, "For a public server. Dex sends no credentials.")
			}
		})
		var authPicker core.Node = integrationsHeading("Registered OAuth client")
		if !isGoogle {
			authPicker = integrationsStack(integrationsLabel("Authentication"), ui.Select[string]{Label: "Authentication", TestID: "integrations-auth", Value: auth.Get, OnChange: auth.Set, Disabled: busy.Get,
				Style: core.Style{Width: core.Pct(100), Height: core.Pt(36)}, Options: []ui.Option[string]{{Value: "none", Label: "No authentication"}, {Value: "header", Label: "HTTP headers"}, {Value: "oauth", Label: "Browser OAuth"}}})
		}
		return integrationsStack(
			integrationsMuted(cx, "Add a Streamable HTTP server for all Bots or for one Bot."),
			target.field(cx, "Target", true, busy.Get), integrationsMuted(cx, "Bots inherit a shared entry unless they override or disable it."),
			integrationsInput(cx, "integrations-mcp-name", "Name", "Letters, digits, - and _.", name, busy.Get),
			integrationsInput(cx, "integrations-mcp-url", "Server URL", "HTTPS, or HTTP on this computer. No user name, password, or query.", address, func() bool { return busy.Get() || isGoogle }),
			authPicker, authFields,
			core.Show{When: func() bool { return attempted.Get() && validation() != "" }, Then: func(cx *core.Scope) core.Node {
				return core.Text{TestID: "integrations-mcp-validation", Role: core.RoleStatus, ContentFunc: validation, Style: core.Style{FontSize: 13}, StyleFunc: ui.Colored(ui.ColorsOf(cx).Danger)}
			}},
			core.Show{When: func() bool { return issue.Get() != "" }, Then: func(cx *core.Scope) core.Node {
				return integrationsFailure(cx, "integrations-mcp-error", "Could not add the server", issue.Get, nil)
			}},
			integrationsActions(ui.Button{Text: "Cancel", TestID: "integrations-mcp-cancel", Variant: ui.ButtonSecondary, Disabled: busy.Get, OnPress: closeSafe},
				ui.Button{Text: "Add server", TestID: "integrations-mcp-save", Busy: busy.Get, BusyLabel: "Adding…", Disabled: func() bool {
					return !target.validShared() || strings.TrimSpace(name.Get()) == "" || strings.TrimSpace(address.Get()) == ""
				}, OnPress: save}),
		)
	})
	return integrationsDialog(cx, "integrations-add-dialog", formTitle, closeSafe, busy.Get, body)
}

func integrationsOAuthFields(cx *core.Scope, client, key, scope *core.Signal[string], locked func() bool) core.Node {
	return integrationsStack(
		integrationsInput(cx, "integrations-oauth-client", "Client ID", "", client, locked),
		integrationsInput(cx, "integrations-oauth-secret", "Client secret vault name", "Use a secret already stored for the selected target. Do not enter the secret value here.", key, locked),
		integrationsInput(cx, "integrations-oauth-scope", "Scope", "Separate scopes with spaces.", scope, locked),
	)
}

func integrationsHeaderFields(cx *core.Scope, headers *core.Signal[[]integrationsHeader], locked func() bool) core.Node {
	return integrationsStack(integrationsMuted(cx, "Dex sends these headers to this server. Check the URL before you add a header. Use an existing vault reference for secrets, such as Bearer ${vault:NAME}."),
		core.Dynamic(func(cx *core.Scope) core.Node {
			// Read the count, not the values typed in the fields, when mounting rows.
			return core.Each(func() []int {
				indices := make([]int, len(headers.Get()))
				for i := range indices {
					indices[i] = i
				}
				return indices
			}, func(i int) int { return i }, func(cx *core.Scope, item func() int, _ func() int) core.Node {
				i := item()
				r := func() integrationsHeader { return headers.Get()[i] }
				set := func(name bool, text string) {
					rows := slices.Clone(headers.Get())
					if i >= len(rows) {
						return
					}
					if name {
						rows[i].Name = text
					} else {
						rows[i].Value = text
					}
					headers.Set(rows)
				}
				id := strconv.Itoa(i + 1)
				return integrationsStack(
					ui.Input{TestID: "integrations-header-name-" + id, Label: "Header name", Placeholder: "Header name", Value: func() string { return r().Name }, ReadonlyFunc: func() bool { return locked() || r().Required }, OnChange: func(s string) { set(true, s) }, AutoComplete: core.AutoCompleteOff},
					ui.Input{TestID: "integrations-header-value-" + id, Label: "Header value", Placeholder: "Header value", Value: func() string { return r().Value }, ReadonlyFunc: locked, OnChange: func(s string) { set(false, s) }, AutoComplete: core.AutoCompleteOff},
					core.Show{When: func() bool { return !r().Required }, Then: func(*core.Scope) core.Node {
						return ui.Button{Text: "Remove", Label: "Remove header " + id, Variant: ui.ButtonGhost, Disabled: locked, OnPress: func() {
							rows := slices.Clone(headers.Get())
							if i < len(rows) {
								headers.Set(slices.Delete(rows, i, i+1))
							}
						}}
					}},
				)
			})
		}),
		ui.Button{Text: "Add header", TestID: "integrations-add-header", Icon: lucide.Plus, Variant: ui.ButtonOutline, Disabled: locked, Style: core.Style{AlignSelf: core.AlignStart}, OnPress: func() { headers.Set(append(slices.Clone(headers.Get()), integrationsHeader{})) }},
	)
}

func integrationsSaved(cx *core.Scope, name string, bot integrationsTarget, close func()) core.Node {
	busy, issue := core.NewSignal(cx, false), core.NewSignal(cx, "")
	pending := core.NewSignal(cx, false)
	server := core.NewSignal[*globalMCP](cx, nil)
	message := "Saved as shared. Bots inherit it unless they override or disable it."
	path := "/api/integrations"
	if bot.ID != "" {
		message = "Saved for " + bot.Name + ". Other Bots do not change."
		path = botPath(bot.ID) + "/integrations"
	}
	load := func() {
		if busy.Get() {
			return
		}
		busy.Set(true)
		issue.Set("")
		work(cx, func(ctx context.Context) (*globalMCP, error) {
			var catalog globalIntegrations
			if err := requestJSON(ctx, http.MethodGet, path, nil, &catalog); err != nil {
				return nil, err
			}
			for _, s := range catalog.Servers {
				if s.Name == name && (bot.ID == "" || s.Scope == "agent") {
					return new(s), nil
				}
			}
			if bot.ID != "" {
				return nil, nil
			}
			return nil, errors.New("The server is saved. Try again to read its status.")
		}, func(s *globalMCP, err error) {
			busy.Set(false)
			if err != nil {
				issue.Set(err.Error())
				return
			}
			server.Set(s)
			pending.Set(s == nil)
		})
	}
	cx.Untrack(load)
	return integrationsStack(integrationsMuted(cx, message),
		core.Show{When: busy.Get, Then: func(cx *core.Scope) core.Node { return integrationsStatus(cx, "Checking server status…") }},
		core.Show{When: func() bool { return issue.Get() != "" }, Then: func(cx *core.Scope) core.Node {
			return integrationsFailure(cx, "integrations-status-error", "Could not load the server status", issue.Get, load)
		}},
		core.Show{When: func() bool { return pending.Get() && issue.Get() == "" }, Then: func(cx *core.Scope) core.Node {
			return integrationsStack(integrationsMuted(cx, "The server appears under MCP servers when "+bot.Name+" is idle."),
				ui.Button{Text: "Refresh status", TestID: "integrations-status-refresh", Variant: ui.ButtonOutline, Busy: busy.Get,
					Style: core.Style{AlignSelf: core.AlignStart}, OnPress: load})
		}},
		core.Dynamic(func(cx *core.Scope) core.Node {
			if s := server.Get(); s != nil {
				status := []core.Node{integrationsNameLine(cx, s.Name, globalMCPLabel(*s))}
				if auth := integrationsAuthorize(cx, bot.ID, name, s.OAuth, s.State); auth != nil {
					status = append(status, auth)
				}
				return integrationsStack(status...)
			}
			return core.Group{}
		}),
		integrationsActions(ui.Button{Text: "Done", TestID: "integrations-done", OnPress: close}),
	)
}

func integrationsAuthorize(cx *core.Scope, bot, name, oauth, state string) core.Node {
	// OAuth uses a browser cookie that a native client cannot pass to the user's browser.
	if runtime.GOOS != "js" || oauth == "" || state == "disabled" {
		return nil
	}
	busy, problem, address := core.NewSignal(cx, false), core.NewSignal(cx, ""), core.NewSignal(cx, "")
	start := func() {
		if busy.Get() {
			return
		}
		busy.Set(true)
		problem.Set("")
		path := "/api/integrations/mcp/" + url.PathEscape(name) + "/oauth"
		var body any
		if bot != "" {
			path = botPath(bot) + "/integrations/mcp/" + url.PathEscape(name) + "/oauth"
			body = struct {
				Scope string `json:"scope"`
			}{"agent"}
		}
		work(cx, func(ctx context.Context) (string, error) {
			var out struct {
				URL string `json:"authorizationUrl"`
			}
			err := requestJSON(ctx, http.MethodPost, path, body, &out)
			if err == nil && !integrationsValidURL(out.URL, false) {
				err = errors.New("The authorization address is not valid.")
			}
			return out.URL, err
		}, func(s string, err error) {
			busy.Set(false)
			if err != nil {
				problem.Set(err.Error())
				return
			}
			address.Set(s)
		})
	}
	label := "Authorize"
	if oauth != "authorize" {
		label = "Reauthorize"
	}
	return integrationsStack(
		core.Show{When: func() bool { return address.Get() == "" }, Then: func(*core.Scope) core.Node {
			return ui.Button{Text: label, TestID: "integrations-authorize", Variant: ui.ButtonOutline, Busy: busy.Get, BusyLabel: "Starting…", OnPress: start, Style: core.Style{AlignSelf: core.AlignStart}}
		}, Else: func(*core.Scope) core.Node {
			return ui.Button{Text: "Open authorization page", TestID: "integrations-open-auth", Variant: ui.ButtonOutline, Style: core.Style{AlignSelf: core.AlignStart}, OnPress: func() {
				cx.OpenURL(address.Get(), func(open bool) {
					if !open {
						problem.Set("Could not open the browser. Try again.")
					}
				})
			}}
		}},
		core.Show{When: func() bool { return problem.Get() != "" }, Then: func(cx *core.Scope) core.Node {
			return integrationsFailure(cx, "integrations-auth-error", "Could not start authorization", problem.Get, nil)
		}},
	)
}

type integrationsSkillAttempt struct {
	bot                  string
	session              int
	message, text, title string
}

func integrationsSkillDialog(cx *core.Scope, q integrationsQuery, skill integrationsSkillHit, close func(), openSession func(string, int, string)) core.Node {
	target := integrationsNewTargets(cx, q)
	level := core.NewSignal(cx, "dex")
	if !q.missing && q.target != "" {
		level.Set("agent")
	}
	name, issue, busy := core.NewSignal(cx, ""), core.NewSignal(cx, ""), core.NewSignal(cx, false)
	var attempt integrationsSkillAttempt
	hasAttempt, complete := core.NewSignal(cx, false), core.NewSignal(cx, false)
	locked := func() bool { return busy.Get() || hasAttempt.Get() }
	closeSafe := func() {
		if !busy.Get() {
			close()
		}
	}
	open := func() {
		if attempt.session == 0 {
			return
		}
		if openSession != nil {
			openSession(attempt.bot, attempt.session, attempt.title)
			return
		}
		_ = cx.SetAppURL("/b/"+url.PathEscape(attempt.bot)+"/s/"+strconv.Itoa(attempt.session), core.AppURLOptions{History: core.HistoryPush})
	}
	begin := func() {
		if busy.Get() || complete.Get() {
			return
		}
		request := attempt
		if !hasAttempt.Get() {
			bot, valid := target.selected()
			if !valid || !target.validShared() {
				return
			}
			text, err := integrationsSkillPrompt(skill, level.Get(), strings.TrimSpace(name.Get()))
			if err != nil {
				issue.Set(err.Error())
				return
			}
			request = integrationsSkillAttempt{bot: bot.ID, message: uuid.New().String(), text: text}
		}
		busy.Set(true)
		issue.Set("")
		work(cx, func(ctx context.Context) (integrationsSkillAttempt, error) {
			if request.session == 0 {
				var created chatSession
				if err := requestJSON(ctx, http.MethodPost, chatSessionsPath(request.bot), nil, &created); err != nil {
					return request, err
				}
				if created.ID <= 0 {
					return request, errors.New("Dex did not return an install Session.")
				}
				request.session, request.title = created.ID, created.Title
			}
			return request, sendChat(ctx, request.bot, request.session, request.message, request.text)
		}, func(result integrationsSkillAttempt, err error) {
			busy.Set(false)
			attempt = result
			hasAttempt.Set(result.session > 0)
			if err != nil {
				issue.Set(err.Error())
				return
			}
			complete.Set(true)
			if openSession != nil {
				open()
			}
		})
	}
	return integrationsDialog(cx, "integrations-install-dialog", func() string { return "Install " + skill.Name }, closeSafe, busy.Get, integrationsStack(
		integrationsMuted(cx, "A Bot reviews the source in a new Session. Follow the review and the install result there."),
		target.field(cx, "Bot", false, locked), integrationsMuted(cx, "This Bot runs the review on its Runner."), integrationsLabel("Install level"),
		ui.Select[string]{Label: "Install level", TestID: "integrations-install-level", Value: level.Get, OnChange: level.Set, Disabled: locked, Style: core.Style{Width: core.Pct(100)},
			Options: []ui.Option[string]{{Value: "dex", Label: "Shared (all Bots)"}, {Value: "agent", Label: "This Bot only"}}},
		integrationsInput(cx, "integrations-install-name", "Directory name", "Optional. Keep the original directory name if blank.", name, locked),
		core.Show{When: func() bool { return issue.Get() != "" }, Then: func(cx *core.Scope) core.Node {
			return integrationsFailure(cx, "integrations-install-error", "Could not start the install", issue.Get, nil)
		}},
		core.Show{When: hasAttempt.Get, Then: func(*core.Scope) core.Node {
			return ui.Button{Text: "Open the install Session", Disabled: busy.Get, OnPress: open, Variant: ui.ButtonOutline}
		}},
		integrationsActions(ui.Button{Text: "Cancel", Variant: ui.ButtonSecondary, Disabled: busy.Get, OnPress: closeSafe}, ui.Button{TestID: "integrations-install-start", TextFunc: func() string {
			if hasAttempt.Get() {
				return "Retry sending"
			}
			return "Start install"
		}, Busy: busy.Get, BusyLabel: "Starting…", Disabled: func() bool {
			_, ok := target.selected()
			return complete.Get() || !hasAttempt.Get() && (!ok || !target.validShared())
		}, OnPress: begin}),
	))
}
