package main

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"uuid"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

type boardAttention struct {
	Sessions []boardQuestionSession `json:"sessions"`
	Setup    []boardSetup           `json:"automations"`
	Reviews  []boardTask            `json:"taskReviews"`
	Results  []boardResult          `json:"results"`
}

type boardQuestionSession struct {
	ID       int            `json:"id"`
	BotID    string         `json:"botId"`
	Title    string         `json:"title"`
	Main     bool           `json:"main"`
	Question *boardQuestion `json:"question"`
}

type boardQuestion struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	Options []string `json:"options"`
}

type boardSetup struct {
	ID     string `json:"id"`
	BotID  string `json:"botId"`
	Name   string `json:"name"`
	Setup  bool   `json:"setup"`
	Target int    `json:"targetSessionId"`
}

type boardHeldList struct {
	Events []boardHeld `json:"events"`
	Total  int         `json:"total"`
}

type boardHeld struct {
	ID          string `json:"id"`
	EventType   string `json:"eventType"`
	Reason      string `json:"reason"`
	Content     string `json:"content"`
	SourceURL   string `json:"sourceUrl"`
	Author      string `json:"authorLogin"`
	Association string `json:"authorAssociation"`
}

func boardEligible(bot, filter string) bool { return filter == "" || bot == filter }

func boardNeedsCount(d boardSnapshot, reviews []boardTask, filter string) int {
	n := len(reviews)
	for _, session := range d.attention.value.Sessions {
		if session.Question != nil && boardEligible(session.BotID, filter) {
			n++
		}
	}
	for _, item := range d.attention.value.Setup {
		if item.Setup && boardEligible(item.BotID, filter) {
			n++
		}
	}
	if filter == "" {
		n += d.held.value.Total
	}
	return n
}

func boardNeeds(cx *core.Scope, data func() boardSnapshot, reviews func() []boardTask, openTask func(string), name func(string) string, nav boardNavigation, refresh func()) core.Node {
	return boardSection(cx, "needs", "Needs you", func() int { return boardNeedsCount(data(), reviews(), nav.FilterBot) }, nil,
		core.Each(func() []boardQuestionSession {
			var rows []boardQuestionSession
			for _, item := range data().attention.value.Sessions {
				if item.Question != nil && boardEligible(item.BotID, nav.FilterBot) {
					rows = append(rows, item)
				}
			}
			return rows
		}, func(s boardQuestionSession) string { return s.BotID + ":" + s.Question.ID }, func(cx *core.Scope, s func() boardQuestionSession, _ func() int) core.Node {
			return boardQuestionRow(cx, s, name, nav.OpenChat, refresh)
		}),
		core.Each(reviews, func(t boardTask) string { return t.Key }, func(cx *core.Scope, task func() boardTask, _ func() int) core.Node {
			return boardReviewRow(cx, task, name, openTask, refresh)
		}),
		core.Each(func() []boardSetup {
			var rows []boardSetup
			for _, item := range data().attention.value.Setup {
				if item.Setup && boardEligible(item.BotID, nav.FilterBot) {
					rows = append(rows, item)
				}
			}
			return rows
		}, func(s boardSetup) string { return s.ID }, func(cx *core.Scope, s func() boardSetup, _ func() int) core.Node {
			colors := ui.ColorsOf(cx)
			button := core.Dynamic(func(*core.Scope) core.Node {
				row := s()
				if row.Target > 0 && nav.OpenChat != nil {
					return ui.Button{Text: "Configure session model", TestID: "board-setup-" + row.ID, Size: ui.SizeSmall, OnPress: func() { nav.OpenChat(row.BotID, row.Target, row.Name) }}
				}
				if row.Target <= 0 && nav.OpenBotSettings != nil {
					return ui.Button{Text: "Configure model", TestID: "board-setup-" + row.ID, Size: ui.SizeSmall, OnPress: func() { nav.OpenBotSettings(row.BotID) }}
				}
				return core.Group{}
			})
			return boardAttentionRow(cx, func() string { return s().Name }, func() string { return name(s().BotID) + " · No available model" },
				ui.Icon(lucide.TriangleAlert, 16, colors.Warn), boardActions(button))
		}),
		core.Each(func() []boardHeld { return data().held.value.Events }, func(h boardHeld) string { return h.ID }, func(cx *core.Scope, event func() boardHeld, _ func() int) core.Node {
			return boardHeldRow(cx, event, func() []boardTask { return data().tasks.value.Tasks }, name, refresh)
		}),
		core.Show{When: func() bool { h := data().held.value; return h.Total > len(h.Events) }, Then: func(cx *core.Scope) core.Node {
			return boardMuted(ui.ColorsOf(cx), func() string {
				h := data().held.value
				return fmt.Sprintf("Showing the newest %d of %d held events.", len(h.Events), h.Total)
			})
		}})
}

func boardAttentionRow(cx *core.Scope, title, caption func() string, icon core.Node, children ...core.Node) core.Node {
	colors := ui.ColorsOf(cx)
	labels := core.View{Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0), Gap: 3}, Children: []core.Node{
		core.Text{ContentFunc: title, Style: core.Style{FontSize: 14, FontWeight: core.FontWeightMedium}},
		boardMuted(colors, caption),
	}}
	heading := []core.Node{labels}
	if icon != nil {
		heading = append([]core.Node{icon}, heading...)
	}
	return core.View{Style: core.Style{Gap: 8, Padding: core.Symmetric(10, 8)}, Children: append([]core.Node{
		core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 10}, Children: heading},
	}, children...)}
}

func boardActions(nodes ...core.Node) core.Node {
	return core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, AlignItems: core.AlignCenter, Gap: 8}, Children: nodes}
}

type boardAction struct {
	cx      *core.Scope
	busy    *core.Signal[bool]
	err     *core.Signal[string]
	refresh func()
}

func boardNewAction(cx *core.Scope, refresh func()) boardAction {
	return boardAction{cx: cx, busy: core.NewSignal(cx, false), err: core.NewSignal(cx, ""), refresh: refresh}
}

func (a boardAction) run(label string, write func(context.Context) error) {
	if a.busy.Get() {
		return
	}
	a.busy.Set(true)
	a.err.Set("")
	work(a.cx, func(ctx context.Context) (struct{}, error) { return struct{}{}, write(ctx) }, func(_ struct{}, err error) {
		a.busy.Set(false)
		if err != nil {
			a.err.Set(label + ": " + err.Error())
		}
		a.refresh()
	})
}

func (a boardAction) notice() core.Node {
	return core.Show{When: func() bool { return a.err.Get() != "" }, Then: func(*core.Scope) core.Node {
		return core.Text{ContentFunc: a.err.Get, Role: core.RoleStatus}
	}}
}

func boardQuestionRow(cx *core.Scope, session func() boardQuestionSession, name func(string) string, open func(string, int, string), refresh func()) core.Node {
	action := boardNewAction(cx, refresh)
	var priorChoice, requestID string
	caption := func() string { row := session(); return name(row.BotID) + " · " + boardQuestionTitle(row) }
	choose := func(choice string) {
		if action.busy.Get() {
			return
		}
		row := session()
		if choice != priorChoice || requestID == "" {
			priorChoice, requestID = choice, uuid.New().String()
		}
		body := map[string]string{"id": requestID, "text": choice, "answersQuestionId": row.Question.ID}
		path := chatSessionPath(row.BotID, row.ID) + "/messages"
		action.run("Could not send answer", func(ctx context.Context) error { return requestJSON(ctx, http.MethodPost, path, body, nil) })
	}
	choices := []core.Node{core.Each(func() []string {
		var choices []string
		for _, option := range session().Question.Options {
			if !slices.Contains(choices, option) {
				choices = append(choices, option)
			}
		}
		return choices
	}, func(choice string) string { return choice }, func(_ *core.Scope, choice func() string, _ func() int) core.Node {
		return ui.Button{TextFunc: choice, Size: ui.SizeSmall, Variant: ui.ButtonSecondary, Disabled: action.busy.Get, OnPress: func() { choose(choice()) }}
	})}
	if open != nil {
		choices = append(choices, ui.Button{Text: "Open session", TestID: "board-question-open-" + session().Question.ID, Size: ui.SizeSmall, Variant: ui.ButtonGhost,
			OnPress: func() {
				row := session()
				id := row.ID
				if row.Main {
					id = 0
				}
				open(row.BotID, id, boardQuestionTitle(row))
			}})
	}
	return boardAttentionRow(cx, func() string { return session().Question.Text }, caption,
		botMark(cx, session().BotID, func() string { return name(session().BotID) }, false), boardActions(choices...), action.notice())
}

func boardQuestionTitle(row boardQuestionSession) string {
	if row.Main {
		return "Main"
	}
	return cmp.Or(row.Title, "Session")
}

func boardReviewRow(cx *core.Scope, task func() boardTask, name func(string) string, open func(string), refresh func()) core.Node {
	action := boardNewAction(cx, refresh)
	key := task().Key
	approve := func() {
		row := task()
		path := boardTasksPath + "/" + url.PathEscape(key)
		action.run("Could not approve task", func(ctx context.Context) error {
			seq := row.ReviewSeq
			if seq == 0 {
				detail, err := boardRead[taskDetail](ctx, path)
				if err != nil {
					return err
				}
				seq = detail.Task.ReviewSeq
			}
			return requestJSON(ctx, http.MethodPost, path+"/review", taskDetailReview{Decision: "approve", ReviewSeq: seq}, nil)
		})
	}
	return boardAttentionRow(cx, func() string { return key + "  " + task().Title }, func() string { return name(task().Assignee) + " · Waiting for your review" },
		botMark(cx, task().Assignee, func() string { return name(task().Assignee) }, false), boardActions(
			ui.Button{Text: "Approve", TestID: "board-approve-" + key, Variant: ui.ButtonPrimary, Size: ui.SizeSmall, Busy: action.busy.Get, OnPress: approve},
			ui.Button{Text: "View", TestID: "board-open-" + key, Variant: ui.ButtonGhost, Size: ui.SizeSmall, OnPress: func() { open(key) }},
		), action.notice())
}

func boardHeldRow(cx *core.Scope, event func() boardHeld, tasks func() []boardTask, name func(string) string, refresh func()) core.Node {
	action := boardNewAction(cx, refresh)
	selected := core.NewSignal(cx, "")
	id := event().ID
	target := func() boardTask {
		for _, row := range tasks() {
			if row.ID == selected.Get() && !boardClosed(row.Status) {
				return row
			}
		}
		return boardTask{}
	}
	write := func(verb string, row boardTask) {
		var body any
		if verb == "attach" {
			body = map[string]string{"botId": row.Assignee, "taskId": row.ID}
		}
		action.run("Could not "+verb+" event", func(ctx context.Context) error {
			return requestJSON(ctx, http.MethodPost, "/api/sources/held-events/"+url.PathEscape(id)+"/"+verb, body, nil)
		})
	}
	return boardAttentionRow(cx, func() string { return event().EventType }, func() string {
		e := event()
		reason := "No matching task"
		if e.Reason == "conflict" {
			reason = "Matches several tasks"
		}
		caption := reason + " · " + cmp.Or(e.Author, "Unknown author")
		if e.Association != "" {
			caption += " · " + e.Association
		}
		return caption
	}, nil,
		core.Text{ContentFunc: func() string { return event().Content }, MaxLines: 3},
		boardActions(
			ui.Select[string]{Label: "Task", Placeholder: "Attach to task…", Value: func() string { return target().ID }, OnChange: selected.Set, Disabled: action.busy.Get,
				OptionsFunc: func() []ui.Option[string] {
					var options []ui.Option[string]
					for _, row := range tasks() {
						if !boardClosed(row.Status) {
							options = append(options, ui.Option[string]{Value: row.ID, Label: row.Key + " · " + row.Title + " (" + name(row.Assignee) + ")"})
						}
					}
					return options
				}},
			ui.Button{Text: "Attach", TestID: "board-held-attach-" + id, Size: ui.SizeSmall, Disabled: func() bool { return action.busy.Get() || target().ID == "" },
				OnPress: func() {
					if row := target(); row.ID != "" {
						write("attach", row)
					}
				}},
			ui.Button{Text: "Dismiss", TestID: "board-held-dismiss-" + id, Size: ui.SizeSmall, Variant: ui.ButtonGhost, Disabled: action.busy.Get, OnPress: func() { write("dismiss", boardTask{}) }},
			core.Show{When: func() bool {
				u, err := url.Parse(event().SourceURL)
				return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
			}, Then: func(cx *core.Scope) core.Node {
				return ui.Button{Text: "Source", Icon: lucide.ExternalLink, Size: ui.SizeSmall, Variant: ui.ButtonGhost, OnPress: func() { cx.OpenURL(event().SourceURL, func(bool) {}) }}
			}},
		), action.notice())
}

func boardFirstLine(text string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(strings.TrimLeft(first, "#"))
}
