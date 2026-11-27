package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
	"uuid"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

type taskDetail struct {
	Task         boardTask           `json:"task"`
	Entries      []taskDetailEntry   `json:"entries"`
	Sessions     []taskDetailSession `json:"sessions"`
	Blockers     []taskDetailBlocker `json:"blockers"`
	Parent       *boardTask          `json:"parentTask"`
	Subtasks     []boardTask         `json:"subtasks"`
	Dependencies []boardTask         `json:"dependencies"`
	Links        []struct {
		Namespace string `json:"namespace"`
		Type      string `json:"type"`
		Key       string `json:"key"`
	} `json:"links"`
}

type taskDetailBlocker struct {
	Kind      string `json:"kind"`
	TaskKey   string `json:"taskKey"`
	ObjectKey string `json:"objectKey"`
	EventType string `json:"eventType"`
	Question  string `json:"question"`
}

type taskDetailEntry struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Actor     string `json:"actorKind"`
	ActorBot  string `json:"actorBotId"`
	Content   string `json:"content"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"createdAt"`
}

type taskDetailSession struct {
	ID      int    `json:"sessionId"`
	BotID   string `json:"botId"`
	BotName string `json:"botName"`
	Title   string `json:"title"`
	Primary bool   `json:"isPrimary"`
	State   string `json:"state"`
}

type taskDetailReview struct {
	Decision  string `json:"decision"`
	ReviewSeq int64  `json:"reviewSeq"`
	Note      string `json:"note,omitempty"`
}

func taskPage(cx *core.Scope, _ string, key string, onChat func(botID string, id int, title string), onBoard ...func()) core.Node {
	path := boardTasksPath + "/" + url.PathEscape(key)
	colors := ui.ColorsOf(cx)
	detail := core.NewSignalFunc(cx, taskDetail{}, nil)
	bots := core.NewSignalFunc(cx, []botListItem(nil), slices.Equal)
	busy := core.NewSignal(cx, "")
	loadError, notice, noticeAction := core.NewSignal(cx, ""), core.NewSignal(cx, ""), core.NewSignal(cx, "")
	draft, composing := core.NewSignal(cx, ""), core.NewSignal(cx, false)
	type submission struct{ id, bot, task, text string }
	var pending submission

	load := func() {
		if busy.Get() != "" {
			return
		}
		busy.Set("load")
		loadError.Set("")
		work(cx, func(ctx context.Context) (taskDetail, error) { return boardRead[taskDetail](ctx, path) }, func(value taskDetail, err error) {
			busy.Set("")
			if err != nil {
				loadError.Set("Could not load task: " + err.Error())
				return
			}
			detail.Set(value)
		})
	}
	work(cx, func(ctx context.Context) (boardRoster, error) { return boardRead[boardRoster](ctx, "/api/bots") }, func(value boardRoster, err error) {
		if err == nil {
			bots.Set(value.Bots)
		}
	})
	name := func(id string) string { return boardBotName(bots.Get(), id) }
	canWrite := func() bool {
		return busy.Get() == "" && loadError.Get() == "" && !composing.Get() && detail.Get().Task.ID != ""
	}
	canPost := func() bool {
		n := utf8.RuneCountInString(strings.TrimSpace(draft.Get()))
		return canWrite() && n > 0 && n <= 32000
	}
	mutate := func(action, fail, success string, run func(context.Context) error, saved func()) {
		if !canWrite() {
			return
		}
		busy.Set(action)
		notice.Set("")
		noticeAction.Set(action)
		work(cx, func(ctx context.Context) (struct{}, error) { return struct{}{}, run(ctx) }, func(_ struct{}, err error) {
			busy.Set("")
			if err != nil {
				notice.Set(fail + ": " + err.Error())
			} else {
				if saved != nil {
					saved()
				}
				notice.Set(success)
			}
			// The next write must use the revision from a new read.
			load()
		})
	}
	patch := func(values map[string]string) {
		revision := detail.Get().Task.Revision
		mutate("status", "Could not update task. Review the refreshed details before trying again", "Task updated.", func(ctx context.Context) error {
			return taskDetailWrite(ctx, http.MethodPatch, path, taskDetailRevision(revision), values)
		}, nil)
	}
	note := func() {
		if !canPost() {
			return
		}
		revision := detail.Get().Task.Revision
		body := map[string]string{"content": strings.TrimSpace(draft.Get())}
		mutate("note", "Could not confirm the note. Check the timeline before trying again", "Note saved.", func(ctx context.Context) error {
			return taskDetailWrite(ctx, http.MethodPost, path+"/entries", taskDetailRevision(revision), body)
		}, func() { draft.Set("") })
	}
	send := func() {
		task := detail.Get().Task
		if !canPost() || boardClosed(task.Status) {
			return
		}
		text := strings.TrimSpace(draft.Get())
		// A retry of the same draft retains its message identifier.
		if pending.id == "" || pending.bot != task.Assignee || pending.task != task.ID || pending.text != text {
			pending = submission{id: uuid.New().String(), bot: task.Assignee, task: task.ID, text: text}
		}
		req := pending
		mutate("send", "Could not confirm the message. Your draft is ready to retry", "Message sent to the assignee's Main.", func(ctx context.Context) error {
			var main struct {
				ID int `json:"id"`
			}
			if err := requestJSON(ctx, http.MethodGet, chatSessionPath(req.bot, 0), nil, &main); err != nil {
				return err
			}
			if main.ID <= 0 {
				return errors.New("the server did not return a valid Main session")
			}
			body := map[string]string{"id": req.id, "text": req.text, "taskId": req.task}
			return requestJSON(ctx, http.MethodPost, chatSessionPath(req.bot, main.ID)+"/messages", body, nil)
		}, func() { pending = submission{}; draft.Set("") })
	}
	review := func(decision string) {
		task := detail.Get().Task
		if task.Status != "in_review" {
			return
		}
		body := taskDetailReview{Decision: decision, ReviewSeq: task.ReviewSeq}
		if decision == "request_changes" {
			if !canPost() {
				return
			}
			body.Note = strings.TrimSpace(draft.Get())
		}
		mutate("review", "Could not record review", "Review saved.", func(ctx context.Context) error {
			return requestJSON(ctx, http.MethodPost, path+"/review", body, nil)
		}, func() {
			if body.Note != "" {
				draft.Set("")
			}
		})
	}
	cx.Untrack(load)
	heading := []core.Node{}
	if len(onBoard) > 0 && onBoard[0] != nil {
		heading = append(heading, boardBackButton(cx, "task-back-board", onBoard[0]))
	}
	heading = append(heading, core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Wrap: core.Wrap, Gap: 8}, Children: []core.Node{
		core.Text{TestID: "task-title", Role: core.RoleHeader, ContentFunc: func() string { return cmp.Or(detail.Get().Task.Title, key) },
			Style: core.Style{FontSize: 24, LineHeight: 30, FontWeight: core.FontWeightSemibold, FlexShrink: 1}},
		core.Text{TestID: "task-key", Content: key, Style: core.Style{FontFamily: "monospace", FontSize: 12, Padding: core.Symmetric(3, 6), BorderWidth: 1, CornerRadius: 4},
			StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border(), Color: colors.Muted()} }},
	}})
	composer := core.View{Style: core.Style{Gap: 10, Padding: core.All(14), BorderWidth: 1, CornerRadius: 8},
		StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border(), Background: colors.Surface()} }, Children: []core.Node{
			ui.Input{TestID: "task-composer", Label: "Task note", Value: draft.Get, Multiline: true, SubmitBehavior: core.SubmitBehaviorNewline,
				Placeholder: "Message the assignee or add a note…", Style: core.Style{Height: core.Pt(80), BorderWidth: core.Zero, Padding: core.All(core.Zero)},
				StyleFunc: ui.Filled(colors.Surface), ReadonlyFunc: func() bool { return busy.Get() != "" }, OnChange: func(value string) { draft.Set(value) },
				OnCompositionChange: func(value core.Composition) { composing.Set(value.Composing) }},
			core.Show{When: func() bool { return utf8.RuneCountInString(strings.TrimSpace(draft.Get())) > 32000 }, Then: func(*core.Scope) core.Node {
				return core.Text{Content: "Notes can contain up to 32,000 characters.", Role: core.RoleStatus}
			}},
			boardActions(ui.Button{TextFunc: func() string {
				if boardClosed(detail.Get().Task.Status) {
					return "Add note"
				}
				return "Note only"
			}, TestID: "task-note", OnPress: note,
				Variant: ui.ButtonSecondary, Busy: func() bool { return busy.Get() == "note" }, Disabled: func() bool { return !canPost() }},
				core.Show{When: func() bool { return !boardClosed(detail.Get().Task.Status) }, Then: func(*core.Scope) core.Node {
					return ui.Button{TextFunc: func() string { return "Send to " + name(detail.Get().Task.Assignee) }, TestID: "task-send", Variant: ui.ButtonPrimary, OnPress: send,
						Busy: func() bool { return busy.Get() == "send" }, Disabled: func() bool { return !canPost() }}
				}},
				core.Show{When: func() bool { return detail.Get().Task.Status == "in_review" }, Then: func(*core.Scope) core.Node {
					return ui.Button{Text: "Request changes", TestID: "task-request-changes", Variant: ui.ButtonOutline, OnPress: func() { review("request_changes") }, Disabled: func() bool { return !canPost() }}
				}}),
			taskDetailNotice("task-action-status", func() bool { return noticeAction.Get() == "note" || noticeAction.Get() == "send" }, notice.Get),
		}}
	return boardFrame("task-page", []core.Node{
		core.View{Style: core.Style{Gap: 8, Margin: core.Edges{Bottom: 8}}, Children: heading},
		core.Show{When: func() bool { return busy.Get() == "load" && detail.Get().Task.ID == "" }, Then: func(*core.Scope) core.Node {
			return core.Text{TestID: "task-loading", Content: "Loading…", Role: core.RoleStatus}
		}},
		core.Show{When: func() bool { return loadError.Get() != "" }, Then: func(*core.Scope) core.Node {
			return core.View{Style: core.Style{Gap: 8}, Children: []core.Node{core.Text{TestID: "task-load-error", ContentFunc: loadError.Get, Role: core.RoleStatus},
				ui.Button{TestID: "task-refresh", Text: "Try again", OnPress: load, Style: core.Style{AlignSelf: core.AlignStart}}}}
		}},
		core.Show{When: func() bool { return detail.Get().Task.ID != "" }, Then: func(cx *core.Scope) core.Node {
			return core.View{Style: core.Style{Gap: 24}, Children: []core.Node{
				taskDetailProperties(cx, detail.Get, bots.Get, name, canWrite, patch),
				taskDetailNotice("task-transition-status", func() bool { return noticeAction.Get() == "status" || noticeAction.Get() == "review" }, notice.Get),
				core.Show{When: func() bool { return detail.Get().Task.Status == "in_review" }, Then: func(*core.Scope) core.Node {
					return boardActions(core.Text{Content: "Waiting for review"}, ui.Button{Text: "Approve", TestID: "task-approve", Variant: ui.ButtonPrimary,
						Disabled: func() bool { return !canWrite() }, OnPress: func() { review("approve") }})
				}},
				taskDetailContext(cx, detail.Get, name, onChat),
				taskDetailTimeline(cx, detail.Get, name),
				composer,
			}}
		}},
	})
}

func taskDetailProperties(cx *core.Scope, detail func() taskDetail, bots func() []botListItem, name func(string) string, allowed func() bool, patch func(map[string]string)) core.Node {
	colors := ui.ColorsOf(cx)
	task := func() boardTask { return detail().Task }
	status := core.Text{TestID: "task-status", ContentFunc: func() string { return boardStatus(task().Status) }, Style: core.Style{FontSize: 12.5, FontWeight: core.FontWeightMedium}}
	statusMenu := ui.Menu{TestID: "task-status-picker", Label: "Status", Placement: core.PlaceBottom, Align: core.AlignStart,
		Trigger: ui.Button{Variant: ui.ButtonOutline, Size: ui.SizeSmall},
		Face:    core.View{Style: core.Style{Direction: core.Row, Gap: 6, Padding: core.Symmetric(5, 8), AlignItems: core.AlignCenter}, Children: []core.Node{status, ui.Icon(lucide.ChevronDown, 12, colors.Muted)}},
		Children: []core.Node{core.Each(func() []string { return taskDetailStatuses(task()) }, func(s string) string { return s }, func(_ *core.Scope, state func() string, _ func() int) core.Node {
			to := state()
			return ui.MenuItem{TestID: "task-status-" + to, Text: boardStatus(to), Disabled: func() bool { return !allowed() }, OnSelect: func() {
				if slices.Contains(taskDetailStatuses(task()), to) {
					patch(map[string]string{"status": to})
				}
			}}
		})}}
	return boardActions(statusMenu,
		core.Show{When: func() bool { return len(bots()) > 0 && !boardClosed(task().Status) }, Then: func(*core.Scope) core.Node {
			return ui.Select[string]{TestID: "task-assignee-picker", Label: "Assignee Bot", Value: func() string { return task().Assignee },
				OptionsFunc: func() []ui.Option[string] { return boardBotOptions(bots()) }, OnChange: func(id string) {
					if id != task().Assignee {
						patch(map[string]string{"assigneeBotId": id})
					}
				},
				Disabled: func() bool { return !allowed() }, Style: core.Style{Width: core.Pt(132), Height: core.Pt(30), FontSize: 12.5}}
		}, Else: func(*core.Scope) core.Node { return boardMuted(colors, func() string { return name(task().Assignee) }) }},
		boardMuted(colors, func() string {
			row := task()
			switch row.Reviewer {
			case "owner":
				return "Reviewer: Owner"
			case "bot":
				return "Reviewer: " + name(row.ReviewerID)
			default:
				return "Reviewer: None"
			}
		}),
		core.Show{When: func() bool { return task().UpdatedAt > 0 }, Then: func(*core.Scope) core.Node {
			return boardMuted(colors, func() string { return "Updated " + boardRelativeTime(task().UpdatedAt) })
		}})
}

func taskDetailContext(cx *core.Scope, detail func() taskDetail, name func(string) string, onChat func(string, int, string)) core.Node {
	colors := ui.ColorsOf(cx)
	return core.View{Style: core.Style{Gap: 12}, Children: []core.Node{
		core.Show{When: func() bool { return detail().Task.Blocked }, Then: func(*core.Scope) core.Node {
			return core.View{TestID: "task-blocked", Style: core.Style{Gap: 4}, Children: []core.Node{core.Text{Content: "Blocked", Role: core.RoleHeader},
				boardMuted(colors, func() string {
					var reasons []string
					for _, row := range detail().Blockers {
						switch row.Kind {
						case "dependency":
							reasons = append(reasons, "Waiting for "+row.TaskKey)
						case "question":
							reasons = append(reasons, cmp.Or(row.Question, "Waiting for an answer"))
						case "wait":
							reasons = append(reasons, strings.TrimSpace(row.EventType+" "+row.ObjectKey))
						}
					}
					return cmp.Or(strings.Join(reasons, "\n"), "This task has blockers.")
				})}}
		}},
		core.Show{When: func() bool { return len(detail().Sessions) > 0 }, Then: func(*core.Scope) core.Node {
			return core.View{Style: core.Style{Gap: 8}, Children: []core.Node{
				core.Text{Content: "Sessions", Role: core.RoleHeader, Style: core.Style{FontSize: 15, FontWeight: core.FontWeightMedium}},
				core.Each(func() []taskDetailSession { return detail().Sessions }, func(s taskDetailSession) int { return s.ID }, func(_ *core.Scope, session func() taskDetailSession, _ func() int) core.Node {
					children := []core.Node{
						core.Text{ContentFunc: func() string { return taskDetailSessionTitle(session()) }},
						boardMuted(colors, func() string { s := session(); return cmp.Or(s.BotName, name(s.BotID)) + " · " + s.State }),
					}
					if onChat != nil {
						children = append(children, ui.Button{Text: "Open session", TestID: "task-open-session-" + strconv.Itoa(session().ID), Size: ui.SizeSmall, Variant: ui.ButtonSecondary,
							Style: core.Style{AlignSelf: core.AlignStart}, OnPress: func() {
								s := session()
								onChat(s.BotID, s.ID, taskDetailSessionTitle(s))
							}})
					}
					return core.View{Style: core.Style{Gap: 4}, Children: children}
				}),
			}}
		}, Else: func(*core.Scope) core.Node {
			children := []core.Node{core.Text{Content: "No linked sessions.", TestID: "task-no-sessions", StyleFunc: ui.Colored(colors.Muted)}}
			if onChat != nil {
				children = append(children, ui.Button{TextFunc: func() string { return "Open " + name(detail().Task.Assignee) + " Main" }, TestID: "task-open-main",
					Variant: ui.ButtonGhost, Size: ui.SizeSmall, Style: core.Style{AlignSelf: core.AlignStart}, OnPress: func() { id := detail().Task.Assignee; onChat(id, 0, name(id)) }})
			}
			return core.View{Style: core.Style{Gap: 4}, Children: children}
		}},
		core.Show{When: func() bool {
			d := detail()
			return d.Parent != nil || len(d.Subtasks)+len(d.Dependencies)+len(d.Links) != 0
		}, Then: func(*core.Scope) core.Node {
			return core.View{Style: core.Style{Gap: 4}, Children: []core.Node{core.Text{Content: "Related work", Role: core.RoleHeader},
				boardMuted(colors, func() string {
					d := detail()
					var lines []string
					if d.Parent != nil {
						lines = append(lines, "Parent · "+d.Parent.Key+" "+d.Parent.Title)
					}
					for _, row := range d.Subtasks {
						lines = append(lines, "Subtask · "+row.Key+" "+row.Title)
					}
					for _, row := range d.Dependencies {
						lines = append(lines, "Depends on · "+row.Key+" "+row.Title)
					}
					for _, row := range d.Links {
						lines = append(lines, row.Namespace+" · "+row.Type+" "+row.Key)
					}
					return strings.Join(lines, "\n")
				})}}
		}},
	}}
}

func taskDetailTimeline(cx *core.Scope, detail func() taskDetail, name func(string) string) core.Node {
	colors := ui.ColorsOf(cx)
	return core.View{Style: core.Style{Gap: 12}, Children: []core.Node{
		core.Text{Role: core.RoleHeader, ContentFunc: func() string { return "Timeline  " + strconv.Itoa(len(detail().Entries)) }, Style: core.Style{FontSize: 15, FontWeight: core.FontWeightMedium}},
		core.Show{When: func() bool { return len(detail().Entries) == 0 }, Then: func(*core.Scope) core.Node {
			return core.Text{TestID: "task-no-entries", Content: "No activity yet.", StyleFunc: ui.Colored(colors.Muted)}
		}},
		core.Each(func() []taskDetailEntry { return detail().Entries }, func(entry taskDetailEntry) int64 { return entry.ID }, func(_ *core.Scope, entry func() taskDetailEntry, _ func() int) core.Node {
			return core.View{TestID: "task-entry-" + strconv.FormatInt(entry().ID, 10), Style: core.Style{Gap: 4, Borders: core.Edges{Left: 1}, Padding: core.Edges{Left: 12}},
				StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} }, Children: []core.Node{
					boardMuted(colors, func() string { return taskDetailEntryLabel(entry(), name) }),
					core.Show{When: func() bool { return entry().Content != "" }, Then: func(*core.Scope) core.Node {
						return core.Text{ContentFunc: func() string { return entry().Content }, Style: core.Style{FontSize: 14}}
					}},
				}}
		}),
	}}
}

func taskDetailNotice(id string, relevant func() bool, text func() string) core.Node {
	return core.Show{When: func() bool { return relevant() && text() != "" }, Then: func(*core.Scope) core.Node {
		return core.Text{TestID: id, ContentFunc: text, Role: core.RoleStatus}
	}}
}

func taskDetailRevision(revision int64) http.Header {
	return http.Header{"If-Match": {strconv.FormatInt(revision, 10)}}
}

func taskDetailWrite(ctx context.Context, method, path string, headers http.Header, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	res, err := gatewayRequest(ctx, method, path, "application/json", bytes.NewReader(data), "application/json", headers)
	if err != nil {
		return err
	}
	closeChatBody(ctx, res.Body)()
	return nil
}

func taskDetailStatuses(task boardTask) []string {
	switch task.Status {
	case "backlog":
		return []string{"ready", "in_progress", "cancelled"}
	case "ready":
		return []string{"backlog", "in_progress", "cancelled"}
	case "in_progress":
		if task.Reviewer != "" {
			return []string{"backlog", "ready", "in_review", "cancelled"}
		}
		return []string{"backlog", "ready", "in_review", "done", "cancelled"}
	case "in_review":
		return []string{"cancelled"}
	case "done", "cancelled":
		return []string{"in_progress"}
	}
	return nil
}

func taskDetailSessionTitle(session taskDetailSession) string {
	return cmp.Or(session.Title, "Session #"+strconv.Itoa(session.ID))
}

func taskDetailEntryLabel(entry taskDetailEntry, name func(string) string) string {
	actor := "External"
	switch entry.Actor {
	case "owner":
		actor = "You"
	case "bot":
		actor = cmp.Or(name(entry.ActorBot), "Bot")
	case "system":
		actor = "System"
	}
	event := map[string]string{"note": "Note", "artifact": "Artifact", "status": "Status", "assign": "Assignment", "review": "Review", "execution": "Execution", "external": "Source"}[entry.Kind]
	if entry.Status != "" {
		event = cmp.Or(event, "Update") + " · " + boardStatus(entry.Status)
	}
	label := actor + " · " + cmp.Or(event, "Update")
	if entry.CreatedAt > 0 {
		label += " · " + boardRelativeTime(entry.CreatedAt)
	}
	return label
}
