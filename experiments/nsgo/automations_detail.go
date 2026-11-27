package main

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

type automationsDetails struct {
	entry             automationsEntry
	runs              []automationsRun
	bot               automationsBot
	blocked           string
	entryErr, runsErr error
}

func automationsLoadDetails(ctx context.Context, bot, id string) automationsDetails {
	var d automationsDetails
	path := automationsPath(bot, id)
	var requests sync.WaitGroup
	requests.Go(func() { d.entryErr = requestJSON(ctx, http.MethodGet, path, nil, &d.entry) })
	requests.Go(func() {
		var result struct {
			Runs []automationsRun `json:"runs"`
		}
		d.runsErr = requestJSON(ctx, http.MethodGet, path+"/runs", nil, &result)
		d.runs = result.Runs
	})
	var botErr error
	requests.Go(func() { botErr = requestJSON(ctx, http.MethodGet, botPath(bot), nil, &d.bot) })
	requests.Wait()
	if d.entryErr != nil {
		return d
	}
	if d.entry.Session == "session" {
		var result struct {
			Automations []struct {
				ID    string `json:"id"`
				Setup bool   `json:"setup"`
			} `json:"automations"`
		}
		if err := requestJSON(ctx, http.MethodGet, "/api/board/attention", nil, &result); err != nil {
			d.blocked = "Could not check the target session model."
		} else {
			for _, a := range result.Automations {
				if a.ID == id && a.Setup {
					d.blocked = "Choose an available chat model first."
					break
				}
			}
		}
		if d.entry.TargetSessionID == 0 {
			d.blocked = "Choose an available target session first."
		}
		return d
	}
	if botErr != nil {
		d.blocked = "Could not check the bot model."
		return d
	}
	if d.bot.ModelID == "" {
		d.blocked = "Choose an available bot model first."
		return d
	}
	var catalog struct {
		Models []struct {
			Provider string `json:"provider"`
			ID       string `json:"id"`
		} `json:"models"`
	}
	if err := requestJSON(ctx, http.MethodGet, "/api/models", nil, &catalog); err != nil {
		d.blocked = "Could not check the bot model."
		return d
	}
	for _, m := range catalog.Models {
		if m.Provider == d.bot.Provider && m.ID == d.bot.ModelID {
			return d
		}
	}
	d.blocked = "Choose an available bot model first."
	return d
}

func automationDetailPage(cx *core.Scope, bot, key string, onChat func(int, string), back func()) core.Node {
	path := automationsPath(bot, key)
	state := core.NewSignal(cx, (*automationsDetails)(nil))
	loading := core.NewSignal(cx, false)
	pending := core.NewSignal(cx, "")
	lastStarted := core.NewSignal(cx, automationsRun{})
	status := core.NewSignal(cx, "")
	editing := core.NewSignal(cx, false)
	deleting := core.NewSignal(cx, false)
	deleteError := core.NewSignal(cx, "")
	load := func() {
		if loading.Get() || pending.Get() != "" {
			return
		}
		loading.Set(true)
		automationsWork(cx, func(ctx context.Context) (automationsDetails, error) {
			return automationsLoadDetails(ctx, bot, key), nil
		}, func(d automationsDetails, _ error) { state.Set(new(d)); loading.Set(false) })
	}
	automationsPoll(cx, load)
	entry := func() automationsEntry {
		if v := state.Get(); v != nil {
			return v.entry
		}
		return automationsEntry{}
	}
	showDetail := func() bool { v := state.Get(); return v != nil && v.entryErr == nil && v.entry.ID != "" }
	locked := func() bool { return pending.Get() != "" || editing.Get() || deleting.Get() }
	install := func(a automationsEntry) {
		if v := state.Get(); v != nil {
			next := *v
			next.entry = a
			state.Set(new(next))
		}
	}
	update := func(patch func(*automationsInput)) {
		if locked() || loading.Get() {
			return
		}
		pending.Set("update")
		status.Set("")
		automationsWork(cx, func(ctx context.Context) (automationsEntry, error) { return automationsUpdate(ctx, path, patch) }, func(a automationsEntry, err error) {
			pending.Set("")
			if err != nil {
				status.Set("Could not change automation: " + err.Error())
				return
			}
			install(a)
			load()
		})
	}
	history := func() []automationsRun {
		if v := state.Get(); v != nil {
			return v.runs
		}
		return nil
	}
	// The POST can return before a poll includes its new record.
	latest := func() automationsRun {
		r := lastStarted.Get()
		if r.ID == "" {
			return r
		}
		if i := slices.IndexFunc(history(), func(item automationsRun) bool { return item.ID == r.ID }); i >= 0 {
			return history()[i]
		}
		return r
	}
	canStart := func() bool {
		// A confirmed terminal history record supersedes the initial running receipt.
		v := state.Get()
		return v != nil && v.entryErr == nil && v.runsErr == nil && v.blocked == "" && !locked() && !loading.Get() && latest().Status != "running" && !slices.ContainsFunc(v.runs, func(r automationsRun) bool { return r.Status == "running" })
	}
	run := func() {
		if !canStart() {
			return
		}
		pending.Set("run")
		status.Set("")
		automationsWork(cx, func(ctx context.Context) (automationsRun, error) {
			var r automationsRun
			err := requestJSON(ctx, http.MethodPost, path+"/run", nil, &r)
			return r, err
		}, func(r automationsRun, err error) {
			pending.Set("")
			if err != nil {
				status.Set("Could not run automation: " + err.Error())
			} else {
				lastStarted.Set(r)
			}
			load()
		})
	}
	remove := func() {
		if pending.Get() != "" {
			return
		}
		pending.Set("delete")
		deleteError.Set("")
		automationsWork(cx, func(ctx context.Context) (struct{}, error) {
			return struct{}{}, requestJSON(ctx, http.MethodDelete, path, nil, nil)
		}, func(_ struct{}, err error) {
			pending.Set("")
			if err != nil {
				deleteError.Set("Could not delete automation: " + err.Error())
				return
			}
			back()
		})
	}
	colors := ui.ColorsOf(cx)
	openEdit := func() {
		if !locked() && showDetail() {
			editing.Set(true)
		}
	}
	primary := core.Show{When: showDetail, Then: func(*core.Scope) core.Node {
		return core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, AlignItems: core.AlignStart, JustifyContent: core.JustifySpaceBetween, Gap: 12}, Children: []core.Node{
			core.View{Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0), Gap: 6}, Children: []core.Node{
				core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, AlignItems: core.AlignCenter, Gap: 8}, Children: []core.Node{
					core.Text{ContentFunc: func() string { return entry().Name }, Role: core.RoleHeader, Style: core.Style{FontSize: 18, FontWeight: core.FontWeightSemibold, FlexShrink: 1, MinWidth: core.Pt(0)}},
					automationsBadge(colors, func() string {
						if entry().Enabled {
							return "On"
						}
						return "Paused"
					}),
				}},
				core.Text{ContentFunc: func() string {
					a := entry()
					fields := []string{cmp.Or(state.Get().bot.Name, "the bot"), automationsScheduleText(a), automationsNextText(a)}
					if a.Cwd != "" {
						fields = append(fields, a.Cwd)
					}
					return strings.Join(fields, " · ")
				}, Style: core.Style{FontSize: 13}, StyleFunc: ui.Colored(colors.Muted)},
			}},
			core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, Gap: 8}, Children: []core.Node{
				ui.Button{Text: "Run now", TestID: "automations-run-" + key, Size: ui.SizeSmall, Busy: func() bool { return pending.Get() == "run" }, BusyLabel: "Starting…", Disabled: func() bool { return !canStart() }, OnPress: run},
				ui.Button{TextFunc: func() string {
					if entry().Enabled {
						return "Pause"
					}
					return "Resume"
				}, TestID: "automations-pause", Size: ui.SizeSmall, Variant: ui.ButtonSecondary, Disabled: func() bool { return locked() || loading.Get() }, OnPress: func() { value := !entry().Enabled; update(func(a *automationsInput) { a.Enabled = value }) }},
				ui.Button{Text: "Edit", TestID: "automations-edit", Size: ui.SizeSmall, Variant: ui.ButtonSecondary, Disabled: locked, OnPress: openEdit},
				ui.Button{Text: "Delete", TestID: "automations-delete", Size: ui.SizeSmall, Variant: ui.ButtonDanger, Disabled: locked, OnPress: func() { deleteError.Set(""); deleting.Set(true) }},
			}},
		}}
	}}
	runList := func() core.Node {
		return core.View{Style: core.Style{MinWidth: core.Pt(0), Gap: 12}, StyleFunc: func() core.Style {
			if cx.Env().Window().Width >= 1100 {
				return core.Style{FlexGrow: 1, FlexShrink: 1, FlexBasis: core.Pt(384)}
			}
			return core.Style{}
		}, Children: []core.Node{
			core.Show{When: func() bool { v := state.Get(); return v != nil && v.runsErr != nil }, Then: func(*core.Scope) core.Node {
				return automationsFailure("Could not load run history", func() error { return state.Get().runsErr }, load)
			}},
			core.Each(func() []int64 { return entry().NextRuns }, func(at int64) int64 { return at }, func(_ *core.Scope, at func() int64, _ func() int) core.Node {
				return core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, JustifyContent: core.JustifySpaceBetween, Gap: 8, Padding: core.All(14), BorderWidth: 1}, StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} }, Children: []core.Node{
					core.Text{ContentFunc: func() string { return automationsTime(at(), entry().TimeZone) }, Style: core.Style{FontSize: 13, FontWeight: core.FontWeightMedium}}, automationsQuiet(colors, "Scheduled"),
				}}
			}),
			core.Each(history, func(r automationsRun) string { return r.ID }, func(_ *core.Scope, r func() automationsRun, _ func() int) core.Node {
				return automationsRunRecord(colors, r, func() string { return entry().TimeZone }, onChat, func() string { return entry().Name })
			}),
			core.Show{When: func() bool { v := state.Get(); return v != nil && v.runsErr == nil && len(v.runs) == 0 }, Then: func(*core.Scope) core.Node {
				text := automationsQuiet(colors, "No runs yet. Use Run now to try the prompt.")
				text.Style.TextAlign = core.TextAlignCenter
				text.Style.Padding = core.Symmetric(20, 0)
				return text
			}},
		}}
	}
	rail := func() core.Node {
		modes := []struct{ value, label, hint string }{
			{"new", "Start fresh", "Each run starts with no earlier conversation. Bot memory still loads."},
			{"reuse", "Continue from last run", "The bot sees earlier runs. The context grows. A busy session skips the run."},
			{"session", "Use an existing session", "Runs join the chosen session as follow-ups."},
		}
		children := []core.Node{
			automationsQuiet(colors, "INSTRUCTIONS"),
			core.Text{ContentFunc: func() string { return entry().Prompt }, Style: core.Style{FontSize: 13}},
			ui.Button{Text: "Edit instructions", TestID: "automations-edit-instructions", Size: ui.SizeSmall, Variant: ui.ButtonOutline, Style: core.Style{AlignSelf: core.AlignStart}, Disabled: locked, OnPress: openEdit},
			core.View{Style: core.Style{Height: core.Pt(1)}, StyleFunc: ui.Filled(colors.Border)}, automationsQuiet(colors, "EACH RUN"),
		}
		for _, m := range modes {
			children = append(children, base.Button{Label: m.label, TestID: "automations-mode-" + m.value, Selected: func() bool { return entry().Session == m.value }, Disabled: func() bool { return locked() || loading.Get() },
				Style: core.Style{Padding: core.All(10), Gap: 4, BorderWidth: 1}, StyleFunc: func() core.Style {
					s := core.Style{BorderColor: colors.Border()}
					if entry().Session == m.value {
						s.Background = colors.Inset()
						s.BorderColor = colors.Text()
					}
					return s
				}, OnPress: func(core.PressEvent) {
					if m.value == "session" {
						openEdit()
						return
					}
					if entry().Session != m.value {
						update(func(a *automationsInput) { a.Session = m.value; a.TargetSessionID = 0 })
					}
				}, Children: []core.Node{core.Text{Content: m.label, Style: core.Style{FontSize: 13, FontWeight: core.FontWeightSemibold}}, automationsQuiet(colors, m.hint)}})
		}
		children = append(children, automationsQuiet(colors, "Each run is still its own run record. This page lists them in one place."))
		return core.View{TestID: "automations-settings-rail", Style: core.Style{Gap: 12, FlexShrink: 0}, StyleFunc: func() core.Style {
			if cx.Env().Window().Width >= 1100 {
				return core.Style{Width: core.Pt(288), Padding: core.Edges{Left: 20}, Borders: core.Edges{Left: 1}, BorderColor: colors.Border()}
			}
			return core.Style{Width: core.Pct(100)}
		}, Children: children}
	}
	return core.View{Style: ui.FillRest, Children: []core.Node{
		automationsPageFrame("automations-detail", true,
			core.View{Style: core.Style{Gap: 8}, Children: []core.Node{
				ui.Button{Text: "Automations", TestID: "automations-back", Variant: ui.ButtonGhost, Size: ui.SizeSmall, Style: core.Style{AlignSelf: core.AlignStart}, OnPress: back},
				primary,
			}},
			core.Show{When: func() bool { return state.Get() == nil }, Then: func(*core.Scope) core.Node { return automationsQuiet(colors, "Loading…") }},
			core.Show{When: func() bool { v := state.Get(); return v != nil && v.entryErr != nil }, Then: func(*core.Scope) core.Node {
				return automationsFailure("Could not load automation", func() error { return state.Get().entryErr }, load)
			}},
			core.Show{When: showDetail, Then: func(*core.Scope) core.Node {
				return core.View{Style: core.Style{Gap: 16}, Children: []core.Node{
					automationsStatus("automations-run-blocked", func() string {
						if b := state.Get().blocked; b != "" {
							return "Run now is off. " + b
						}
						return ""
					}),
					automationsStatus("automations-detail-status", status.Get),
					core.Show{When: func() bool { return latest().ID != "" }, Then: func(*core.Scope) core.Node {
						return core.View{Style: core.Style{Gap: 6, Padding: core.All(12), CornerRadius: 6}, StyleFunc: ui.Filled(colors.Inset), Children: []core.Node{
							core.Text{ContentFunc: func() string { return "Run now · " + automationsRunLabel(latest()) }, TestID: "automations-run-result", Role: core.RoleStatus},
							automationsStatus("automations-run-reason", func() string { return latest().Reason }),
						}}
					}},
					core.View{TestID: "automations-runs", Style: core.Style{Gap: 24}, StyleFunc: func() core.Style {
						if cx.Env().Window().Width >= 1100 {
							return core.Style{Direction: core.Row, AlignItems: core.AlignStart}
						}
						return core.Style{}
					}, Children: []core.Node{runList(), rail()}},
				}}
			}},
		),
		core.Show{When: editing.Get, Then: func(form *core.Scope) core.Node {
			current := core.Untracked(form, entry)
			return automationsForm(form, bot, &current, func() { editing.Set(false) }, func(a automationsEntry) { install(a); editing.Set(false); load() })
		}},
		core.Show{When: deleting.Get, Then: func(*core.Scope) core.Node {
			cancel := func() {
				if pending.Get() == "" {
					deleting.Set(false)
				}
			}
			return ui.Dialog{Title: "Delete automation?", TestID: "automations-delete-dialog", Description: "This deletes the automation and its run history. Its sessions stay available.", Open: func() bool { return true },
				OnOpenChange: func(open bool, _ base.Reason) {
					if !open {
						cancel()
					}
				}, Children: []core.Node{automationsStatus("automations-delete-error", deleteError.Get)},
				Footer: []core.Node{ui.Button{Text: "Cancel", Variant: ui.ButtonSecondary, Disabled: func() bool { return pending.Get() != "" }, OnPress: cancel}, ui.Button{Text: "Delete", TestID: "automations-confirm-delete", Variant: ui.ButtonDanger, Busy: func() bool { return pending.Get() == "delete" }, OnPress: remove}}}
		}},
	}}
}

func automationsRunRecord(colors ui.Colors, run func() automationsRun, zone func() string, onChat func(int, string), title func() string) core.Node {
	id := run().ID
	return core.View{TestID: "automations-record-" + id, Style: core.Style{BorderWidth: 1, Padding: core.All(14), Gap: 9}, StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} }, Children: []core.Node{
		core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, AlignItems: core.AlignCenter, Gap: 8}, Children: []core.Node{
			core.Text{ContentFunc: func() string { r := run(); return automationsTime(cmp.Or(r.DueAt, r.CreatedAt), zone()) }, Style: core.Style{FontSize: 13, FontWeight: core.FontWeightSemibold}},
			core.Text{ContentFunc: func() string {
				r := run()
				if r.DueAt != 0 {
					return "Planned · recorded " + automationsTime(r.CreatedAt, zone())
				}
				return "Started by you"
			}, Style: core.Style{FontSize: 11.5}, StyleFunc: ui.Colored(colors.Muted)},
			automationsBadge(colors, func() string { return automationsRunLabel(run()) }),
			core.Show{When: func() bool { return run().Duplicate }, Then: func(*core.Scope) core.Node { return automationsBadge(colors, func() string { return "duplicate" }) }},
		}},
		core.Show{When: func() bool { return run().AdmittedAt != nil }, Then: func(*core.Scope) core.Node {
			return core.Text{ContentFunc: func() string {
				r := run()
				if r.AdmittedAt == nil {
					return ""
				}
				label := "Received " + automationsTime(*r.AdmittedAt, zone())
				if r.SettledAt != nil && *r.SettledAt >= *r.AdmittedAt {
					label += fmt.Sprintf(" · %s until settled", (time.Duration(*r.SettledAt-*r.AdmittedAt) * time.Millisecond).Round(time.Millisecond))
				}
				return label
			}, Style: core.Style{FontSize: 12}, StyleFunc: ui.Colored(colors.Muted)}
		}},
		automationsStatus("automations-summary-"+id, func() string { r := run(); return cmp.Or(r.Summary, r.Reason) }),
		automationsStatus("automations-notification-"+id, func() string { return run().NotificationNote }),
		core.Show{When: func() bool { r := run(); return onChat != nil && r.SessionID > 0 && r.Status != "running" }, Then: func(*core.Scope) core.Node {
			return ui.Button{Text: "Run transcript", TestID: "automations-chat-" + id, Variant: ui.ButtonGhost, Size: ui.SizeSmall, Style: core.Style{AlignSelf: core.AlignEnd}, OnPress: func() { onChat(run().SessionID, title()) }}
		}},
	}}
}
