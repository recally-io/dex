package main

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

type automationsSchedule struct {
	Kind    string `json:"kind"`
	Minutes int    `json:"minutes,omitzero"`
	Time    string `json:"time,omitempty"`
	Days    []int  `json:"days,omitempty"`
	At      int64  `json:"at,omitzero"`
}

type automationsNotification struct {
	Mode     string `json:"mode"`
	TargetID string `json:"targetId,omitempty"`
}

type automationsInput struct {
	Name            string                   `json:"name"`
	Prompt          string                   `json:"prompt"`
	Cwd             string                   `json:"cwd"`
	Schedule        automationsSchedule      `json:"schedule"`
	TimeZone        string                   `json:"timeZone"`
	Session         string                   `json:"session"`
	TargetSessionID int64                    `json:"targetSessionId,omitzero"`
	Enabled         bool                     `json:"enabled"`
	RunnerID        string                   `json:"runnerId"`
	Notification    *automationsNotification `json:"notification,omitempty"`
}

type automationsEntry struct {
	automationsInput
	ID       string          `json:"id"`
	NextRuns []int64         `json:"nextRuns"`
	LastRun  *automationsRun `json:"lastRun"`
}

type automationsRun struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	Outcome          string `json:"outcome"`
	Summary          string `json:"summary"`
	Reason           string `json:"reason"`
	NotificationNote string `json:"notificationNote"`
	CreatedAt        int64  `json:"createdAt"`
	DueAt            int64  `json:"dueAt"`
	AdmittedAt       *int64 `json:"admittedAt"`
	SettledAt        *int64 `json:"settledAt"`
	SessionID        int    `json:"sessionId"`
	Duplicate        bool   `json:"duplicate"`
}

type automationsBot struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	ModelID  string `json:"modelId"`
}

func automationsPath(bot, key string) string {
	path := botPath(bot) + "/automations"
	if key != "" {
		path += "/" + url.PathEscape(key)
	}
	return path
}

// A menu can close during its event. Start work under the page or form scope.
func automationsWork[T any](cx *core.Scope, run func(context.Context) (T, error), done func(T, error)) {
	cx.Post(func() { work(cx, run, done) })
}

func automationsPoll(cx *core.Scope, load func()) {
	var next func()
	next = func() {
		load()
		cx.AfterFunc(10*time.Second, next)
	}
	cx.Post(next)
}

func automationsUpdate(ctx context.Context, path string, patch func(*automationsInput)) (automationsEntry, error) {
	var current automationsEntry
	if err := requestJSON(ctx, http.MethodGet, path, nil, &current); err != nil {
		return current, err
	}
	input := current.automationsInput
	patch(&input)
	// Dex retains the existing notification when this field is not sent.
	input.Notification = nil
	var saved automationsEntry
	err := requestJSON(ctx, http.MethodPut, path, input, &saved)
	return saved, err
}

func automationsPage(cx *core.Scope, botID string, onChat func(int, string), openDetail ...func(string)) core.Node {
	if len(openDetail) > 0 && openDetail[0] != nil {
		return automationsListPage(cx, botID, openDetail[0])
	}
	selected := core.NewSignal(cx, "")
	return core.Dynamic(func(page *core.Scope) core.Node {
		if key := selected.Get(); key != "" {
			return core.Untracked(page, func() core.Node {
				return automationDetailPage(page, botID, key, onChat, func() { selected.Set("") })
			})
		}
		return core.Untracked(page, func() core.Node { return automationsListPage(page, botID, selected.Set) })
	})
}

func automationsListPage(cx *core.Scope, botID string, open func(string)) core.Node {
	type snapshot struct {
		rows            []automationsEntry
		bot             automationsBot
		listErr, botErr error
	}
	state := core.NewSignal(cx, (*snapshot)(nil))
	loading := core.NewSignal(cx, false)
	writing := core.NewSignal(cx, "")
	creating := core.NewSignal(cx, false)
	writeError := core.NewSignal(cx, "")
	var revision int
	load := func() {
		if loading.Get() || writing.Get() != "" {
			return
		}
		loading.Set(true)
		startedAt := revision
		automationsWork(cx, func(ctx context.Context) (snapshot, error) {
			var result snapshot
			var list struct {
				Automations []automationsEntry `json:"automations"`
			}
			var requests sync.WaitGroup
			requests.Go(func() { result.listErr = requestJSON(ctx, http.MethodGet, automationsPath(botID, ""), nil, &list) })
			requests.Go(func() { result.botErr = requestJSON(ctx, http.MethodGet, botPath(botID), nil, &result.bot) })
			requests.Wait()
			result.rows = list.Automations
			return result, nil
		}, func(result snapshot, _ error) {
			loading.Set(false)
			if startedAt == revision {
				state.Set(new(result))
			}
		})
	}
	automationsPoll(cx, load)
	rows := func() []automationsEntry {
		if s := state.Get(); s != nil {
			return s.rows
		}
		return nil
	}
	canCreate := func() bool {
		s := state.Get()
		return s != nil && s.botErr == nil && s.bot.ModelID != "" && writing.Get() == ""
	}
	change := func(id string, enabled bool) {
		if writing.Get() != "" {
			return
		}
		revision++
		writing.Set(id)
		writeError.Set("")
		automationsWork(cx, func(ctx context.Context) (automationsEntry, error) {
			return automationsUpdate(ctx, automationsPath(botID, id), func(input *automationsInput) { input.Enabled = enabled })
		}, func(saved automationsEntry, err error) {
			writing.Set("")
			if err != nil {
				writeError.Set("Could not change automation: " + err.Error())
				return
			}
			if prior := state.Get(); prior != nil {
				next := *prior
				next.rows = slices.Clone(prior.rows)
				if i := slices.IndexFunc(next.rows, func(a automationsEntry) bool { return a.ID == saved.ID }); i >= 0 {
					next.rows[i] = saved
				}
				state.Set(new(next))
			}
		})
	}
	colors := ui.ColorsOf(cx)
	return core.View{Style: ui.FillRest, Children: []core.Node{
		automationsPageFrame("automations-list", false,
			core.View{Style: core.Style{Gap: 6}, Children: []core.Node{
				automationsHeading("Automations"), automationsQuiet(colors, "Prompts that the bot runs on a schedule. Each run has its own record."),
			}},
			core.View{Style: core.Style{Gap: 12}, Children: []core.Node{
				core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, AlignItems: core.AlignCenter, JustifyContent: core.JustifySpaceBetween, Gap: 12}, StyleFunc: func() core.Style {
					if cx.Env().Window().Width < 560 {
						return core.Style{Gap: 2}
					}
					return core.Style{}
				}, Children: []core.Node{
					core.View{Style: core.Style{Gap: 4, MinWidth: core.Pt(244)}, Children: []core.Node{
						core.Text{Content: "All automations", Role: core.RoleHeader, Style: core.Style{FontSize: 15, FontWeight: core.FontWeightSemibold}},
						core.Text{ContentFunc: func() string {
							list := rows()
							on := 0
							for _, a := range list {
								if a.Enabled {
									on++
								}
							}
							return fmt.Sprintf("%d of %d on. Open one to see its runs.", on, len(list))
						}, Style: core.Style{FontSize: 12}, StyleFunc: ui.Colored(colors.Muted)},
					}},
					ui.Button{Text: "New automation", TestID: "automations-new", Style: core.Style{Width: core.Pt(123), Height: core.Pt(32)}, Disabled: func() bool { return !canCreate() }, OnPress: func() {
						if canCreate() {
							creating.Set(true)
						}
					}},
				}},
				core.Show{When: func() bool { v := state.Get(); return v != nil && v.botErr != nil }, Then: func(*core.Scope) core.Node {
					return automationsFailure("Could not check bot model", func() error { return state.Get().botErr }, load)
				}},
				automationsStatus("automations-write-status", writeError.Get),
				core.Show{When: func() bool { return state.Get() == nil }, Then: func(*core.Scope) core.Node { return automationsQuiet(colors, "Loading…") }},
				core.Show{When: func() bool { v := state.Get(); return v != nil && v.listErr != nil }, Then: func(*core.Scope) core.Node {
					return automationsFailure("Could not load automations", func() error { return state.Get().listErr }, load)
				}},
				core.Show{When: func() bool { v := state.Get(); return v != nil && v.listErr == nil && len(v.rows) == 0 }, Then: func(*core.Scope) core.Node {
					return core.View{Style: core.Style{AlignItems: core.AlignCenter, Gap: 6, Padding: core.All(24), BorderWidth: 1, CornerRadius: 8}, StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} }, Children: []core.Node{
						core.Text{Content: "No automations yet", Role: core.RoleHeader, Style: core.Style{FontWeight: core.FontWeightSemibold}},
						core.Text{ContentFunc: func() string {
							if s := state.Get(); s != nil && s.botErr == nil && s.bot.ModelID == "" {
								return "Choose a bot model first. Automations use the same model as every session."
							}
							return "Create one to run a prompt every few minutes, every day, on some weekdays, or once."
						}, Style: core.Style{TextAlign: core.TextAlignCenter, FontSize: 13}, StyleFunc: ui.Colored(colors.Muted)},
					}}
				}},
				core.Show{When: func() bool { return len(rows()) > 0 }, Then: func(*core.Scope) core.Node {
					return core.View{Style: core.Style{BorderWidth: 1, CornerRadius: 8}, StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} }, Children: []core.Node{
						core.Each(rows, func(a automationsEntry) string { return a.ID }, func(_ *core.Scope, row func() automationsEntry, i func() int) core.Node {
							id := row().ID
							return core.View{Style: core.Style{Direction: core.Row, Gap: 12, AlignItems: core.AlignStart, Padding: core.Symmetric(12, 16)}, StyleFunc: func() core.Style {
								s := core.Style{BorderColor: colors.Border()}
								if i() > 0 {
									s.Borders.Top = 1
								}
								return s
							}, Children: []core.Node{
								core.View{Role: core.RoleLink, TestID: "automations-open-" + id, LabelFunc: func() string { return row().Name },
									Web:   core.WebViewProps{Href: "/b/" + url.PathEscape(botID) + "/automations/" + url.PathEscape(id)},
									Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0), Gap: 3}, OnPress: func(core.PressEvent) { open(id) }, Children: []core.Node{
										core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, Gap: 8, AlignItems: core.AlignCenter}, Children: []core.Node{
											core.Text{ContentFunc: func() string { return row().Name }, Style: core.Style{FontSize: 13.5, FontWeight: core.FontWeightMedium, MinWidth: core.Pt(0), FlexShrink: 1}},
											core.Show{When: func() bool { r := row().LastRun; return r != nil && r.Status != "done" }, Then: func(*core.Scope) core.Node {
												return automationsBadge(colors, func() string {
													r := row().LastRun
													if r == nil {
														return ""
													}
													return "Last run " + strings.ToLower(automationsRunLabel(*r))
												})
											}},
										}},
										core.Text{ContentFunc: func() string {
											a := row()
											return automationsScheduleText(a) + "   " + automationsSessionText(a.Session) + "   " + automationsNextText(a)
										}, Style: core.Style{FontSize: 12}, StyleFunc: ui.Colored(colors.Muted)},
									}},
								ui.Switch{TestID: "automations-toggle-" + id, LabelFunc: func() string { return "Enable " + row().Name }, Size: ui.SizeSmall, Checked: func() bool { return row().Enabled }, Disabled: func() bool { return writing.Get() != "" }, OnChange: func(enabled bool) { change(id, enabled) }},
							}}
						}),
					}}
				}},
			}},
		),
		core.Show{When: creating.Get, Then: func(form *core.Scope) core.Node {
			return automationsForm(form, botID, nil, func() { creating.Set(false) }, func(saved automationsEntry) {
				revision++
				if prior := state.Get(); prior != nil {
					next := *prior
					if !slices.ContainsFunc(next.rows, func(row automationsEntry) bool { return row.ID == saved.ID }) {
						next.rows = append(slices.Clone(next.rows), saved)
						state.Set(new(next))
					}
				}
				creating.Set(false)
				load()
			})
		}},
	}}
}

func automationsPageFrame(id string, wide bool, children ...core.Node) core.Node {
	width := core.Points(816)
	if wide {
		width = 1120
	}
	return core.Scroll{TestID: id, Style: ui.FillRest, Children: []core.Node{
		core.View{Style: core.Style{Width: core.Pct(100), MaxWidth: core.Pt(width), Padding: core.Edges{Top: 24, Bottom: 40, Left: 24, Right: 24}, Gap: 24}, Children: children},
	}}
}

func automationsHeading(title string) core.Text {
	return core.Text{Content: title, Role: core.RoleHeader, Style: core.Style{FontSize: 18, FontWeight: core.FontWeightSemibold, MinWidth: core.Pt(0)}}
}

func automationsQuiet(colors ui.Colors, text string) core.Text {
	return core.Text{Content: text, Style: core.Style{FontSize: 13, MinWidth: core.Pt(0)}, StyleFunc: ui.Colored(colors.Muted)}
}

func automationsBadge(colors ui.Colors, value func() string) core.Node {
	return core.Text{ContentFunc: value, Style: core.Style{FontSize: 11.5, CornerRadius: 10, Padding: core.Symmetric(2, 8)}, StyleFunc: func() core.Style { return core.Style{Background: colors.Inset(), Color: colors.Muted()} }}
}

func automationsStatus(id string, value func() string) core.Node {
	return core.Show{When: func() bool { return value() != "" }, Then: func(*core.Scope) core.Node { return core.Text{TestID: id, ContentFunc: value, Role: core.RoleStatus} }}
}

func automationsFailure(title string, err func() error, retry func()) core.Node {
	nodes := []core.Node{core.Text{ContentFunc: func() string {
		if cause := err(); cause != nil {
			return title + ": " + cause.Error()
		}
		return title
	}, Role: core.RoleStatus}}
	if retry != nil {
		nodes = append(nodes, ui.Button{Text: "Try again", Variant: ui.ButtonOutline, Size: ui.SizeSmall, OnPress: retry})
	}
	return core.View{Style: core.Style{Gap: 6, AlignItems: core.AlignStart}, Children: nodes}
}

var automationsWeekdays = [...]string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

func automationsTime(millis int64, zone string) string {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		loc = time.UTC
	}
	return time.UnixMilli(millis).In(loc).Format("Mon 2 Jan, 15:04")
}

func automationsScheduleText(a automationsEntry) string {
	s := a.Schedule
	switch s.Kind {
	case "every":
		return fmt.Sprintf("Every %d minutes", s.Minutes)
	case "daily":
		return "Every day at " + s.Time
	case "once":
		return "Once on " + automationsTime(s.At, a.TimeZone)
	case "weekly":
		days := slices.Sorted(slices.Values(s.Days))
		var names []string
		for _, day := range days {
			if day >= 1 && day <= 7 {
				names = append(names, automationsWeekdays[day-1])
			}
		}
		what := strings.Join(names, ", ")
		if len(names) == 7 {
			what = "day"
		} else if slices.Equal(days, []int{1, 2, 3, 4, 5}) {
			what = "weekday"
		}
		return "Every " + what + " at " + s.Time
	default:
		return ""
	}
}

func automationsSessionText(session string) string {
	switch session {
	case "session":
		return "Existing session"
	case "reuse":
		return "One session"
	default:
		return "New session each run"
	}
}

func automationsNextText(a automationsEntry) string {
	if len(a.NextRuns) > 0 {
		return "next " + automationsTime(a.NextRuns[0], a.TimeZone)
	}
	if a.Enabled {
		return "done"
	}
	return "paused"
}

func automationsRunLabel(run automationsRun) string {
	if run.Status == "done" {
		switch run.Outcome {
		case "changed":
			return "Changed"
		case "unchanged":
			return "Unchanged"
		case "failed":
			return "Failed"
		}
		return "Done"
	}
	switch run.Status {
	case "running":
		return "Running"
	case "failed":
		return "Failed"
	case "skipped":
		return "Skipped"
	}
	return cmp.Or(run.Status, "Unknown")
}
