package main

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

type boardResult struct {
	ID           string `json:"runId"`
	BotID        string `json:"botId"`
	BotName      string `json:"botName"`
	AutomationID string `json:"automationId"`
	Name         string `json:"automationName"`
	Session      int    `json:"sessionId"`
	Outcome      string `json:"outcome"`
	Summary      string `json:"summary"`
	At           int64  `json:"createdAt"`
	Read         bool   `json:"read"`
	Dismissed    bool   `json:"dismissed"`
}

func boardVisibleResults(results []boardResult, filter string) []boardResult {
	var out []boardResult
	for _, result := range results {
		if boardEligible(result.BotID, filter) {
			out = append(out, result)
		}
	}
	return out
}

type boardResultGroup struct {
	id      string
	results []boardResult
}

func boardResultChange(ctx context.Context, id, action string) error {
	return requestJSON(ctx, http.MethodPost, "/api/automations/results/"+url.PathEscape(id)+"/"+action, nil, nil)
}

func boardDismissResults(ctx context.Context, list []boardResult) error {
	for _, row := range list {
		if err := boardResultChange(ctx, row.ID, "dismiss"); err != nil {
			return err
		}
	}
	return nil
}

func boardUpdates(cx *core.Scope, results func() []boardResult, name func(string) string, nav boardNavigation, refresh func()) core.Node {
	visible := func() []boardResult { return boardVisibleResults(results(), nav.FilterBot) }
	groups := func() []boardResultGroup {
		var groups []boardResultGroup
		indexes := make(map[string]int)
		for _, result := range visible() {
			index, exists := indexes[result.AutomationID]
			if !exists {
				index = len(groups)
				indexes[result.AutomationID] = index
				groups = append(groups, boardResultGroup{id: result.AutomationID})
			}
			groups[index].results = append(groups[index].results, result)
		}
		return groups
	}
	all := boardNewAction(cx, refresh)
	action := core.Show{When: func() bool { return len(visible()) > 1 }, Then: func(*core.Scope) core.Node {
		return ui.Button{Text: "Mark all done", TestID: "board-updates-done", Variant: ui.ButtonGhost, Size: ui.SizeSmall, Busy: all.busy.Get, OnPress: func() {
			list := visible()
			all.run("Could not clear updates", func(ctx context.Context) error { return boardDismissResults(ctx, list) })
		}}
	}}
	return boardSection(cx, "updates", "Updates", func() int { return len(visible()) }, action,
		all.notice(), core.Each(groups, func(g boardResultGroup) string { return g.id }, func(cx *core.Scope, g func() boardResultGroup, _ func() int) core.Node {
			return boardUpdateRow(cx, g, name, nav, refresh)
		}))
}

func boardUpdateRow(cx *core.Scope, group func() boardResultGroup, name func(string) string, nav boardNavigation, refresh func()) core.Node {
	colors := ui.ColorsOf(cx)
	open := core.NewSignal(cx, false)
	action := boardNewAction(cx, refresh)
	latest := func() boardResult { return group().results[0] }
	toggle := func() {
		open.Set(!open.Get())
		row := latest()
		if open.Get() && !row.Read {
			action.run("Could not mark result as read", func(ctx context.Context) error { return boardResultChange(ctx, row.ID, "read") })
		}
	}
	return boardAttentionRow(cx, func() string {
		list := group().results
		label := list[0].Name
		if len(list) > 1 {
			label += " ×" + strconv.Itoa(len(list))
		}
		return label
	}, func() string { row := latest(); return name(row.BotID) + " · " + boardFirstLine(row.Summary) },
		botMark(cx, latest().BotID, func() string { return name(latest().BotID) }, false), boardActions(
			ui.Button{TestID: "board-update-" + group().id, TextFunc: func() string {
				if open.Get() {
					return "Hide details"
				}
				return "Show details"
			},
				Icon: func() *core.SVG {
					if open.Get() {
						return lucide.ChevronDown()
					}
					return lucide.ChevronRight()
				},
				Size: ui.SizeSmall, Variant: ui.ButtonGhost, OnPress: toggle},
			ui.Button{Text: "Mark done", TestID: "board-update-done-" + group().id, Size: ui.SizeSmall, Variant: ui.ButtonGhost, Busy: action.busy.Get, OnPress: func() {
				list := group().results
				action.run("Could not mark update done", func(ctx context.Context) error { return boardDismissResults(ctx, list) })
			}},
		), action.notice(), core.Show{When: open.Get, Then: func(*core.Scope) core.Node {
			return core.Each(func() []boardResult { return group().results }, func(row boardResult) string { return row.ID }, func(_ *core.Scope, result func() boardResult, _ func() int) core.Node {
				return core.View{Style: core.Style{Gap: 6, Padding: core.All(10), BorderWidth: 1, CornerRadius: 8},
					StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} }, Children: []core.Node{
						boardMuted(colors, func() string { return boardResultStatus(result()) }),
						core.Text{ContentFunc: func() string { return result().Summary }},
						boardResultLinks(result, nav.OpenResult, nav.OpenChat),
					}}
			})
		}})
}

func boardResultStatus(row boardResult) string {
	var outcome string
	switch row.Outcome {
	case "failed":
		outcome = "Failed"
	case "changed":
		outcome = "Changed"
	default:
		outcome = row.Outcome
	}
	if row.At == 0 {
		return outcome
	}
	return outcome + " · " + boardRelativeTime(row.At)
}

func boardResultLinks(result func() boardResult, details func(string), chat func(string, int, string)) core.Node {
	var children []core.Node
	if details != nil {
		children = append(children, ui.Button{Text: "View result", Size: ui.SizeSmall, Variant: ui.ButtonGhost,
			OnPress: func() { details(result().ID) }})
	}
	if chat != nil {
		children = append(children, core.Show{When: func() bool { return result().Session > 0 }, Then: func(*core.Scope) core.Node {
			return ui.Button{Text: "Open session", Size: ui.SizeSmall, OnPress: func() { row := result(); chat(row.BotID, row.Session, row.Name) }}
		}})
	}
	return boardActions(children...)
}

func boardResultPage(cx *core.Scope, runID string, onBoard func(), onChat func(botID string, id int, title string)) core.Node {
	colors := ui.ColorsOf(cx)
	result := core.NewSignal(cx, boardResult{})
	busy := core.NewSignal(cx, false)
	problem := core.NewSignal(cx, "")
	load := func() {
		if busy.Get() {
			return
		}
		busy.Set(true)
		problem.Set("")
		work(cx, func(ctx context.Context) (boardResult, error) {
			list, err := boardRead[struct {
				Results []boardResult `json:"results"`
			}](ctx, "/api/automations/results?includeDismissed=true")
			if err != nil {
				return boardResult{}, err
			}
			for _, row := range list.Results {
				if row.ID == runID {
					return row, nil
				}
			}
			return boardResult{}, fmt.Errorf("result not found")
		}, func(row boardResult, err error) {
			busy.Set(false)
			if err != nil {
				problem.Set("Could not load result: " + err.Error())
				return
			}
			result.Set(row)
		})
	}
	cx.Untrack(load)
	action := boardNewAction(cx, load)
	children := []core.Node{}
	if onBoard != nil {
		children = append(children, boardBackButton(cx, "board-result-back", onBoard))
	}
	children = append(children, core.Text{Role: core.RoleHeader, ContentFunc: func() string { return cmp.Or(result.Get().Name, "Automation result") },
		Style: core.Style{FontSize: 24, FontWeight: core.FontWeightSemibold}},
		core.Show{When: busy.Get, Then: func(*core.Scope) core.Node { return core.Text{Content: "Loading…", Role: core.RoleStatus} }},
		core.Show{When: func() bool { return problem.Get() != "" }, Then: func(*core.Scope) core.Node {
			return core.View{Style: core.Style{Gap: 8}, Children: []core.Node{core.Text{ContentFunc: problem.Get, Role: core.RoleStatus},
				ui.Button{Text: "Try again", OnPress: load, Style: core.Style{AlignSelf: core.AlignStart}}}}
		}},
		core.Show{When: func() bool { return result.Get().ID != "" }, Then: func(*core.Scope) core.Node {
			return core.View{Style: core.Style{Gap: 12}, Children: []core.Node{
				boardMuted(colors, func() string {
					row := result.Get()
					return cmp.Or(row.BotName, row.BotID) + " · " + boardResultStatus(row)
				}),
				core.Text{TestID: "board-result-summary", ContentFunc: func() string { return result.Get().Summary }},
				boardResultLinks(result.Get, nil, onChat),
				core.Show{When: func() bool { return !result.Get().Dismissed }, Then: func(*core.Scope) core.Node {
					return boardActions(core.Show{When: func() bool { return !result.Get().Read }, Then: func(*core.Scope) core.Node {
						return ui.Button{Text: "Mark read", TestID: "board-result-read", Disabled: action.busy.Get, OnPress: func() {
							action.run("Could not mark result read", func(ctx context.Context) error { return boardResultChange(ctx, runID, "read") })
						}}
					}}, ui.Button{Text: "Mark done", TestID: "board-result-done", Busy: action.busy.Get, OnPress: func() {
						action.run("Could not mark result done", func(ctx context.Context) error { return boardResultChange(ctx, runID, "dismiss") })
					}})
				}}, action.notice(),
			}}
		}})
	return boardFrame("board-result-page", children)
}

func boardBackButton(cx *core.Scope, id string, onBack func()) core.Node {
	colors := ui.ColorsOf(cx)
	return base.Button{TestID: id, Label: "Back to Board", Role: core.RoleLink, OnPress: func(core.PressEvent) { onBack() },
		Style:   core.Style{Height: core.Pt(18), AlignSelf: core.AlignStart, JustifyContent: core.JustifyCenter},
		HitSlop: core.Edges{Top: 5, Bottom: 5}, Children: []core.Node{
			core.Text{Content: "← Board", Style: core.Style{FontSize: 12, LineHeight: 15}, StyleFunc: ui.Colored(colors.Muted)},
		}}
}
