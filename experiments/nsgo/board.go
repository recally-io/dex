package main

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

const boardTasksPath = "/api/tasks"

type boardTask struct {
	ID         string `json:"id"`
	Key        string `json:"key"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	Assignee   string `json:"assigneeBotId"`
	Reviewer   string `json:"reviewerKind"`
	ReviewerID string `json:"reviewerId"`
	ReviewSeq  int64  `json:"reviewSeq"`
	Revision   int64  `json:"revision"`
	UpdatedAt  int64  `json:"updatedAt"`
	Blocked    bool   `json:"blocked"`
}

type boardNavigation struct {
	FilterBot       string
	ClearFilter     func()
	OpenChat        func(botID string, id int, title string)
	OpenBotSettings func(botID string)
	OpenResult      func(runID string)
}

type boardData[T any] struct {
	value T
	err   error
	ready bool
}

func boardRead[T any](ctx context.Context, path string) (out T, err error) {
	err = requestJSON(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

func boardReadInto[T any](ctx context.Context, path string, prior boardData[T]) boardData[T] {
	value, err := boardRead[T](ctx, path)
	prior.err = err
	if err == nil {
		prior.value, prior.ready = value, true
	}
	return prior
}

type boardRoster struct {
	Bots []botListItem `json:"bots"`
}
type boardTaskList struct {
	Tasks []boardTask `json:"tasks"`
}

type boardSnapshot struct {
	bots      boardData[boardRoster]
	tasks     boardData[boardTaskList]
	attention boardData[boardAttention]
	held      boardData[boardHeldList]
}

func boardStatus(status string) string {
	switch status {
	case "backlog":
		return "Backlog"
	case "ready":
		return "Ready"
	case "in_progress":
		return "In Progress"
	case "in_review":
		return "In Review"
	case "done":
		return "Done"
	case "cancelled":
		return "Cancelled"
	default:
		return status
	}
}

func boardClosed(status string) bool { return status == "done" || status == "cancelled" }

func boardBotName(bots []botListItem, id string) string {
	for _, bot := range bots {
		if bot.ID == id {
			return cmp.Or(bot.Name, id)
		}
	}
	return id
}

func boardPage(cx *core.Scope, botID string, onTask func(key string), navigation ...boardNavigation) core.Node {
	var nav boardNavigation
	if len(navigation) != 0 {
		nav = navigation[0]
	}
	colors := ui.ColorsOf(cx)
	data := core.NewSignalFunc(cx, boardSnapshot{}, nil)
	busy := core.NewSignal(cx, false)
	var again bool
	query := url.Values{"includeClosed": {"true"}}
	if nav.FilterBot != "" {
		query.Set("botId", nav.FilterBot)
	}
	taskPath := boardTasksPath + "?" + query.Encode()
	var load func()
	load = func() {
		if busy.Get() {
			again = true
			return
		}
		busy.Set(true)
		previous := data.Get()
		work(cx, func(ctx context.Context) (out boardSnapshot, _ error) {
			var group sync.WaitGroup
			group.Go(func() { out.bots = boardReadInto(ctx, "/api/bots", previous.bots) })
			group.Go(func() { out.tasks = boardReadInto(ctx, taskPath, previous.tasks) })
			group.Go(func() { out.attention = boardReadInto(ctx, "/api/board/attention", previous.attention) })
			if nav.FilterBot == "" {
				group.Go(func() { out.held = boardReadInto(ctx, "/api/sources/held-events", previous.held) })
			}
			group.Wait()
			return out, nil
		}, func(next boardSnapshot, _ error) {
			data.Set(next)
			busy.Set(false)
			if again {
				again = false
				load()
			}
		})
	}
	var poll func()
	poll = func() {
		load()
		cx.AfterFunc(10*time.Second, poll)
	}
	cx.Untrack(poll)
	bots := func() []botListItem { return data.Get().bots.value.Bots }
	name := func(id string) string { return boardBotName(bots(), id) }
	groups := core.NewMemoFunc(cx, func() map[string][]boardTask {
		d := data.Get()
		result := make(map[string][]boardTask)
		seen := make(map[string]bool)
		for _, task := range d.tasks.value.Tasks {
			seen[task.Key] = true
			group := "active"
			switch {
			case boardClosed(task.Status):
				group = "completed"
			case task.Status == "in_review" && task.Reviewer == "owner":
				group = "needs"
			case task.Blocked:
				group = "blocked"
			case task.Status == "backlog":
				group = "later"
			}
			result[group] = append(result[group], task)
		}
		// Attention can still identify reviews while the task list is unavailable.
		if d.tasks.err != nil {
			for _, review := range d.attention.value.Reviews {
				if !seen[review.Key] && (nav.FilterBot == "" || nav.FilterBot == review.Assignee) {
					result["needs"] = append(result["needs"], review)
				}
			}
		}
		return result
	}, nil)
	rows := func(group string) func() []boardTask { return func() []boardTask { return groups.Get()[group] } }
	header := []core.Node{boardHeading("Board")}
	if nav.FilterBot != "" {
		if nav.ClearFilter != nil {
			header = append(header, ui.Button{TestID: "board-clear-filter", TextFunc: func() string { return name(nav.FilterBot) }, Label: "Clear Bot filter",
				Icon: lucide.X, Size: ui.SizeSmall, Variant: ui.ButtonSecondary, OnPress: nav.ClearFilter})
		} else {
			header = append(header, boardMuted(colors, func() string { return name(nav.FilterBot) }))
		}
	}
	errorsText := func() string {
		d := data.Get()
		var messages []string
		for _, s := range []struct {
			label string
			err   error
		}{{"tasks", d.tasks.err}, {"attention items", d.attention.err}, {"held events", d.held.err}, {"Bots", d.bots.err}} {
			if s.err != nil {
				messages = append(messages, "Could not load "+s.label+": "+s.err.Error())
			}
		}
		return strings.Join(messages, "\n")
	}
	completed := core.NewSignal(cx, false)
	return boardFrame("board-page", []core.Node{
		core.View{Style: core.Style{Direction: core.Row, JustifyContent: core.JustifySpaceBetween, AlignItems: core.AlignCenter, Wrap: core.Wrap, Gap: 8,
			Margin: core.Edges{Bottom: 8}}, Children: header},
		boardQuickAdd(cx, cmp.Or(nav.FilterBot, botID), bots, load),
		core.Show{When: func() bool { d := data.Get(); return busy.Get() && !d.tasks.ready && !d.attention.ready }, Then: func(*core.Scope) core.Node {
			return core.Text{TestID: "board-loading", Content: "Loading…", Role: core.RoleStatus}
		}},
		core.Show{When: func() bool { return errorsText() != "" }, Then: func(*core.Scope) core.Node {
			return core.View{Style: core.Style{Gap: 8}, Children: []core.Node{
				core.Text{TestID: "board-load-error", ContentFunc: errorsText, Role: core.RoleStatus},
				ui.Button{TestID: "board-refresh", Text: "Try again", Disabled: busy.Get, OnPress: load, Style: core.Style{AlignSelf: core.AlignStart}},
			}}
		}},
		boardNeeds(cx, data.Get, rows("needs"), onTask, name, nav, load),
		boardTaskGroup(cx, "blocked", "Blocked", rows("blocked"), name, onTask),
		boardTaskGroup(cx, "active", "Active", rows("active"), name, onTask),
		boardUpdates(cx, func() []boardResult { return data.Get().attention.value.Results }, name, nav, load),
		boardTaskGroup(cx, "later", "Later", rows("later"), name, onTask),
		core.Show{When: func() bool { return len(rows("completed")()) != 0 }, Then: func(cx *core.Scope) core.Node {
			return core.View{TestID: "board-group-completed", Style: core.Style{Gap: 4}, Children: []core.Node{
				ui.Button{TestID: "board-completed", TextFunc: func() string { return fmt.Sprintf("Completed  %d", len(rows("completed")())) },
					Icon: func() *core.SVG {
						if completed.Get() {
							return lucide.ChevronDown()
						}
						return lucide.ChevronRight()
					},
					Variant: ui.ButtonGhost, Size: ui.SizeSmall, Style: core.Style{AlignSelf: core.AlignStart}, OnPress: func() { completed.Set(!completed.Get()) }},
				core.Show{When: completed.Get, Then: func(cx *core.Scope) core.Node { return boardTaskRows(cx, rows("completed"), name, onTask) }},
			}}
		}},
		core.Show{When: func() bool {
			d := data.Get()
			return d.tasks.ready && d.attention.ready && (nav.FilterBot != "" || d.held.ready) && errorsText() == "" &&
				len(d.tasks.value.Tasks) == 0 && boardNeedsCount(d, nil, nav.FilterBot) == 0 && len(boardVisibleResults(d.attention.value.Results, nav.FilterBot)) == 0
		}, Then: func(*core.Scope) core.Node {
			return core.Text{TestID: "board-empty", Content: "All caught up.", StyleFunc: ui.Colored(colors.Muted)}
		}},
	})
}

func boardQuickAdd(cx *core.Scope, defaultBot string, bots func() []botListItem, refresh func()) core.Node {
	colors := ui.ColorsOf(cx)
	title, selected := core.NewSignal(cx, ""), core.NewSignal(cx, "")
	saving, composing := core.NewSignal(cx, false), core.NewSignal(cx, false)
	status := core.NewSignal(cx, "")
	assignee := func() string {
		items := bots()
		if id := selected.Get(); slices.ContainsFunc(items, func(bot botListItem) bool { return bot.ID == id }) {
			return id
		}
		if slices.ContainsFunc(items, func(bot botListItem) bool { return bot.ID == defaultBot }) {
			return defaultBot
		}
		if len(items) > 0 {
			return items[0].ID
		}
		return ""
	}
	canAdd := func() bool {
		length := utf8.RuneCountInString(strings.TrimSpace(title.Get()))
		return !saving.Get() && !composing.Get() && assignee() != "" && length > 0 && length <= 160
	}
	add := func() {
		if !canAdd() {
			return
		}
		body := map[string]string{"title": strings.TrimSpace(title.Get()), "assigneeBotId": assignee(), "status": "ready"}
		saving.Set(true)
		status.Set("")
		work(cx, func(ctx context.Context) (struct{}, error) {
			return struct{}{}, requestJSON(ctx, http.MethodPost, boardTasksPath, body, nil)
		}, func(_ struct{}, err error) {
			saving.Set(false)
			if err != nil {
				status.Set("Could not confirm task creation. Check the list before trying again. " + err.Error())
			} else {
				title.Set("")
			}
			refresh()
		})
	}
	assigneeWidth := core.NewMemo(cx, func() core.Points {
		width := 118
		for _, bot := range bots() {
			width = max(width, 32+7*utf8.RuneCountInString(bot.Name))
		}
		return core.Points(min(width, 144))
	})
	return core.View{Style: core.Style{Gap: 8}, Children: []core.Node{
		core.View{TestID: "board-quick-add", Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8,
			Padding: core.Symmetric(6, 12), MinHeight: core.Pt(46), CornerRadius: 8, BorderWidth: 1},
			StyleFunc: func() core.Style { return core.Style{Background: colors.Surface(), BorderColor: colors.Border()} }, Children: []core.Node{
				ui.Icon(lucide.Plus, 16, colors.Muted),
				ui.Input{TestID: "board-task-title", Label: "Task title", Placeholder: "New task, press Enter to create…", Value: title.Get, ReadonlyFunc: saving.Get,
					Style:     core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0), Height: core.Pt(32), Padding: core.All(core.Zero), BorderWidth: core.Zero, FontSize: 14},
					StyleFunc: ui.Filled(colors.Surface), OnChange: func(value string) { title.Set(value) }, OnSubmit: func(string) { add() },
					OnCompositionChange: func(value core.Composition) { composing.Set(value.Composing) }},
				core.Show{When: func() bool { return len(bots()) > 0 }, Then: func(*core.Scope) core.Node {
					return core.Dynamic(func(*core.Scope) core.Node {
						return ui.Select[string]{TestID: "board-assignee", Label: "Assignee Bot", Value: assignee, OnChange: selected.Set, Disabled: saving.Get,
							Style: core.Style{Width: core.Pt(assigneeWidth.Get()), Height: core.Pt(28), FontSize: 12.5, BorderWidth: core.Zero,
								Background: ui.Alpha(colors.Inset(), .6), Color: colors.Muted()}, OptionsFunc: func() []ui.Option[string] { return boardBotOptions(bots()) }}
					})
				}},
				core.Show{When: func() bool { return strings.TrimSpace(title.Get()) != "" }, Then: func(*core.Scope) core.Node {
					return ui.Button{TestID: "board-create", Text: "Add", Size: ui.SizeSmall, Variant: ui.ButtonPrimary,
						Busy: saving.Get, Disabled: func() bool { return !canAdd() }, OnPress: add}
				}},
			}},
		core.Show{When: func() bool { return utf8.RuneCountInString(strings.TrimSpace(title.Get())) > 160 || status.Get() != "" }, Then: func(*core.Scope) core.Node {
			return core.Text{TestID: "board-create-error", Role: core.RoleStatus, ContentFunc: func() string {
				if utf8.RuneCountInString(strings.TrimSpace(title.Get())) > 160 {
					return "Task titles can contain up to 160 characters."
				}
				return status.Get()
			}}
		}},
	}}
}

func boardBotOptions(bots []botListItem) []ui.Option[string] {
	options := make([]ui.Option[string], 0, len(bots))
	for _, bot := range bots {
		options = append(options, ui.Option[string]{Value: bot.ID, Label: cmp.Or(bot.Name, bot.ID)})
	}
	return options
}

func boardFrame(id string, children []core.Node) core.Node {
	return core.Scroll{TestID: id, Style: ui.FillRest.Merge(core.Style{FlexBasis: core.Pt(0)}), Children: []core.Node{core.View{
		Style: core.Style{Width: core.Pct(100), MaxWidth: core.Pt(816), Padding: core.Symmetric(32, 24), Gap: 24}, Children: children,
	}}}
}

func boardHeading(title string) core.Text {
	return core.Text{Content: title, Role: core.RoleHeader, Style: core.Style{FontSize: 24, LineHeight: 30, FontWeight: core.FontWeightSemibold}}
}

func boardMuted(colors ui.Colors, content func() string) core.Text {
	return core.Text{ContentFunc: content, Style: core.Style{FontSize: 12.5}, StyleFunc: ui.Colored(colors.Muted)}
}

func boardSection(cx *core.Scope, id, title string, count func() int, action core.Node, children ...core.Node) core.Node {
	colors := ui.ColorsOf(cx)
	return core.Show{When: func() bool { return count() > 0 }, Then: func(*core.Scope) core.Node {
		labels := []core.Node{
			core.Text{Content: title, Role: core.RoleHeader, Style: core.Style{FontSize: 12.5, LineHeight: 18.75, FontWeight: core.FontWeightMedium}, StyleFunc: ui.Colored(colors.Muted)},
			core.Text{ContentFunc: func() string { return strconv.Itoa(count()) }, Style: core.Style{FontSize: 12, FontFamily: "monospace"}, StyleFunc: ui.Colored(colors.Muted)},
		}
		if action != nil {
			labels = append(labels, core.View{Style: core.Style{FlexGrow: 1}}, action)
		}
		heading := core.View{Style: core.Style{Direction: core.Row, Gap: 8, AlignItems: core.AlignCenter,
			Padding: core.Edges{Bottom: 8}, Margin: core.Edges{Bottom: 4}, Borders: core.Edges{Bottom: 1}}, StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} }, Children: labels}
		return core.View{TestID: "board-group-" + id, Children: append([]core.Node{heading}, children...)}
	}}
}

func boardTaskGroup(cx *core.Scope, id, title string, tasks func() []boardTask, name func(string) string, open func(string)) core.Node {
	return boardSection(cx, id, title, func() int { return len(tasks()) }, nil, boardTaskRows(cx, tasks, name, open))
}

func boardTaskRows(cx *core.Scope, tasks func() []boardTask, name func(string) string, open func(string)) core.Node {
	colors := ui.ColorsOf(cx)
	return core.Each(tasks, func(task boardTask) string { return task.Key }, func(cx *core.Scope, task func() boardTask, index func() int) core.Node {
		wide := func() bool { return cx.Env().Window().Width >= 640 }
		return core.View{StyleFunc: func() core.Style {
			if index() == 0 {
				return core.Style{}
			}
			return core.Style{Borders: core.Edges{Top: 1}, BorderColor: colors.Border()}
		}, Children: []core.Node{base.Button{TestID: "board-open-" + task().Key, Role: core.RoleLink,
			LabelFunc: func() string { return task().Key + " " + task().Title }, OnPress: func(core.PressEvent) { open(task().Key) },
			Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, MinHeight: core.Pt(41.25), Gap: 12, Padding: core.Symmetric(10, 8), CornerRadius: 6},
			StyleFunc: func() core.Style {
				return core.Style{Hover: &core.Style{Background: colors.Hover()}, Focus: &core.Style{Background: colors.Hover()}}
			},
			Children: []core.Node{
				core.View{Style: core.Style{Width: core.Pt(8), Height: core.Pt(8), CornerRadius: 4}, StyleFunc: func() core.Style {
					t := task()
					if t.Blocked {
						return core.Style{Background: colors.Warn()}
					}
					if t.Status == "ready" {
						return core.Style{BorderWidth: 1.5, BorderColor: colors.Muted()}
					}
					if t.Status == "in_progress" || t.Status == "done" {
						return core.Style{Background: colors.Up()}
					}
					return core.Style{Background: colors.Faint()}
				}},
				core.Text{ContentFunc: func() string { return task().Key }, MaxLines: 1, Style: core.Style{Width: core.Pt(40), FontSize: 12, FontFamily: "monospace"}, StyleFunc: ui.Colored(colors.Muted)},
				core.Text{ContentFunc: func() string { return task().Title }, MaxLines: 1, Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0),
					FontSize: 14, FontWeight: core.FontWeightMedium}, StyleFunc: func() core.Style {
					if boardClosed(task().Status) {
						return core.Style{Color: colors.Muted()}
					}
					return core.Style{Color: colors.Text()}
				}},
				core.Text{ContentFunc: func() string {
					t := task()
					if t.Blocked && !boardClosed(t.Status) {
						return "Blocked"
					}
					if t.Status == "in_review" {
						return "Bot review"
					}
					return boardStatus(t.Status)
				}, MaxLines: 1, Style: core.Style{FontSize: 11.5, LineHeight: 17.25, CornerRadius: 4, Padding: core.Symmetric(2, 6)},
					StyleFunc: func() core.Style { return core.Style{Background: colors.Inset(), Color: colors.Muted()} }},
				core.Dynamic(func(scope *core.Scope) core.Node {
					id := task().Assignee
					return core.Untracked(scope, func() core.Node { return botMark(scope, id, func() string { return name(id) }, false) })
				}),
				core.Show{When: wide, Then: func(*core.Scope) core.Node {
					return core.Text{ContentFunc: func() string { return name(task().Assignee) }, MaxLines: 1, Style: core.Style{MaxWidth: core.Pt(96), FontSize: 12.5}, StyleFunc: ui.Colored(colors.Muted)}
				}},
				core.Show{When: func() bool { return cx.Env().Window().Width >= 768 && task().UpdatedAt > 0 }, Then: func(*core.Scope) core.Node {
					return boardMuted(colors, func() string { return "· " + boardRelativeTime(task().UpdatedAt) })
				}},
			},
		}}}
	})
}

func boardRelativeTime(ms int64) string {
	elapsed := time.Since(time.UnixMilli(ms))
	minutes := int(elapsed.Round(time.Minute) / time.Minute)
	hours := int(elapsed.Round(time.Hour) / time.Hour)
	days := hours / 24
	label := func(count int, unit string) string {
		if count != 1 {
			unit += "s"
		}
		return strconv.Itoa(count) + " " + unit + " ago"
	}
	switch {
	case minutes < 1:
		return "less than a minute ago"
	case minutes < 45:
		return label(minutes, "minute")
	case hours < 24:
		return "about " + label(max(1, hours), "hour")
	case days < 30:
		return label(days, "day")
	case days < 365:
		return label(days/30, "month")
	default:
		return "about " + label(days/365, "year")
	}
}
