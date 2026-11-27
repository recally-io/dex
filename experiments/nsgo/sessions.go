package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

// newWorkspaceSession inherits the displayed session's project. A zero session ID selects Main.
func newWorkspaceSession(ctx context.Context, botID string, sessionID int) (chatSession, error) {
	var project struct {
		Cwd string `json:"cwd"`
	}
	if err := requestJSON(ctx, http.MethodGet, chatSessionPath(botID, sessionID), nil, &project); err != nil {
		return chatSession{}, fmt.Errorf("read current project: %w", err)
	}
	var created chatSession
	if err := requestJSON(ctx, http.MethodPost, chatSessionsPath(botID), nil, &created); err != nil {
		return chatSession{}, err
	}
	if created.ID <= 0 {
		return chatSession{}, errors.New("the new session has no valid ID")
	}
	// Dex can reuse an empty session, so write an empty cwd to clear its previous project too.
	var saved chatSession
	if err := requestJSON(ctx, http.MethodPut, chatSessionPath(botID, created.ID)+"/project", project, &saved); err != nil {
		return chatSession{}, fmt.Errorf("set the new session project: %w", err)
	}
	if saved.ID != created.ID {
		return chatSession{}, errors.New("the project response does not match the new session")
	}
	return saved, nil
}

type sessionsRow struct {
	chatSession    `json:",inline"`
	UpdatedAt      int64  `json:"updatedAt"`
	LastActivityAt int64  `json:"lastActivityAt"`
	ToolStartedAt  int64  `json:"toolStartedAt"`
	ParentID       int    `json:"parentId"`
	ParentBotName  string `json:"parentBotName"`
}

func (row sessionsRow) kind() string {
	switch {
	case row.Main:
		return "Main"
	case row.Channel == "web":
		return "Session"
	case row.Channel == "automation":
		return "Automation"
	default:
		return "Channel"
	}
}

func sessionsPage(cx *core.Scope, botID string, onOpen func(bot string, id int, title string)) core.Node {
	colors := ui.ColorsOf(cx)
	rows := core.NewSignalFunc(cx, []sessionsRow(nil), slices.Equal)
	name := core.NewSignal(cx, "dex")
	mainID := core.NewSignal(cx, 0)
	loaded := core.NewSignal(cx, false)
	loading := core.NewSignal(cx, false)
	writing := core.NewSignal(cx, 0)
	loadError := core.NewSignal(cx, "")
	writeError := core.NewSignal(cx, "")
	status := core.NewSignal(cx, "")
	kind := core.NewSignal(cx, "All kinds")
	archivedOpen := core.NewSignal(cx, false)
	now := core.NewSignal(cx, time.Now().UnixMilli())

	load := func() {
		if loading.Get() || writing.Get() != 0 {
			return
		}
		loading.Set(true)
		work(cx, func(ctx context.Context) ([]sessionsRow, error) {
			var list struct {
				Sessions []sessionsRow `json:"sessions"`
			}
			err := requestJSON(ctx, http.MethodGet, chatSessionsPath(botID), nil, &list)
			return list.Sessions, err
		}, func(fetched []sessionsRow, err error) {
			loading.Set(false)
			now.Set(time.Now().UnixMilli())
			if err != nil {
				loadError.Set(err.Error())
				return
			}
			rows.Set(fetched)
			loadError.Set("")
			loaded.Set(true)
		})
	}
	cx.Post(func() {
		var poll func()
		poll = func() { load(); cx.AfterFunc(3*time.Second, poll) }
		poll()
		work(cx, func(ctx context.Context) (string, error) {
			var bot struct {
				Name string `json:"name"`
			}
			err := requestJSON(ctx, http.MethodGet, botPath(botID), nil, &bot)
			return bot.Name, err
		}, func(value string, err error) {
			if err == nil && value != "" {
				name.Set(value)
			}
		})
		work(cx, func(ctx context.Context) (chatSession, error) { return chatGetSession(ctx, botID, 0) },
			func(session chatSession, err error) {
				if err == nil {
					mainID.Set(session.ID)
				}
			})
	})

	changeArchive := func(row sessionsRow) {
		if writing.Get() != 0 || row.kind() != "Session" || !row.writable() {
			return
		}
		method, verb := http.MethodPut, "Archive"
		if row.ArchivedAt != 0 {
			method, verb = http.MethodDelete, "Restore"
		}
		owner := cmp.Or(row.BotID, botID)
		writing.Set(row.ID)
		writeError.Set("")
		status.Set("")
		work(cx, func(ctx context.Context) (chatSession, error) {
			var saved chatSession
			err := requestJSON(ctx, method, chatSessionPath(owner, row.ID)+"/archive", nil, &saved)
			if err == nil && (saved.ID != row.ID || (saved.ArchivedAt != 0) != (method == http.MethodPut)) {
				err = errors.New("the response did not confirm the archive state")
			}
			return saved, err
		}, func(saved chatSession, err error) {
			writing.Set(0)
			if err != nil {
				writeError.Set("Could not change session. " + err.Error())
				return
			}
			list := slices.Clone(rows.Get())
			if i := slices.IndexFunc(list, func(it sessionsRow) bool { return it.ID == row.ID }); i >= 0 {
				list[i].ArchivedAt = saved.ArchivedAt
				rows.Set(list)
			}
			status.Set(verb + "d session.")
		})
	}

	active := core.NewMemoFunc(cx, func() []sessionsRow {
		return slices.Collect(func(yield func(sessionsRow) bool) {
			for _, row := range rows.Get() {
				if row.ArchivedAt == 0 && !yield(row) {
					return
				}
			}
		})
	}, slices.Equal)
	visible := func() []sessionsRow {
		if kind.Get() == "All kinds" {
			return active.Get()
		}
		return slices.DeleteFunc(slices.Clone(active.Get()), func(row sessionsRow) bool { return row.kind() != kind.Get() })
	}
	archived := func() []sessionsRow {
		return slices.DeleteFunc(slices.Clone(rows.Get()), func(row sessionsRow) bool { return row.ArchivedAt == 0 })
	}
	list := func(items func() []sessionsRow) core.Node {
		return core.View{Style: core.Style{CornerRadius: 8, BorderWidth: 1, Overflow: core.Hidden},
			StyleFunc: func() core.Style { return core.Style{Background: colors.Surface(), BorderColor: colors.Border()} }, Children: []core.Node{
				core.Each(items, func(row sessionsRow) int { return row.ID }, func(_ *core.Scope, item func() sessionsRow, index func() int) core.Node {
					id := strconv.Itoa(item().ID)
					title := func() string { return cmp.Or(item().Title, "New session") }
					label := func() string {
						row := item()
						detail := row.kind()
						if row.ParentID != 0 {
							parent := row.ParentBotName
							if parent == "" && row.ParentID == mainID.Get() {
								parent = "Main"
							}
							if parent == "" {
								if i := slices.IndexFunc(rows.Get(), func(session sessionsRow) bool { return session.ID == row.ParentID }); i >= 0 {
									parent = cmp.Or(rows.Get()[i].Title, "New session")
								}
							}
							detail = "Started by " + cmp.Or(parent, "a session")
						}
						if row.BotName != "" {
							detail += " · " + row.BotName
						}
						if row.UpdatedAt > 0 {
							at := time.UnixMilli(row.UpdatedAt).Local()
							format := "15:04"
							current := time.UnixMilli(now.Get()).Local()
							if at.Year() != current.Year() || at.YearDay() != current.YearDay() {
								format = "2 Jan, 15:04"
							}
							detail += " · " + at.Format(format)
						}
						return detail
					}
					labelGroup := func() []core.Node {
						return []core.Node{
							core.Text{ContentFunc: title, Style: core.Style{FontSize: 13.5, FontWeight: core.FontWeightMedium, LineHeight: 20}, MaxLines: 2},
							core.Text{ContentFunc: label, Style: core.Style{FontSize: 12, LineHeight: 18}, StyleFunc: ui.Colored(colors.Muted)},
						}
					}
					return core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Padding: core.Symmetric(14, 16), Gap: 12},
						StyleFunc: func() core.Style {
							if index() == 0 {
								return core.Style{}
							}
							return core.Style{Borders: core.Edges{Top: 1}, BorderColor: colors.Border()}
						}, Children: []core.Node{
							core.Dynamic(func(*core.Scope) core.Node {
								if item().kind() == "Automation" {
									return ui.Icon(lucide.Clock, 16, colors.Muted)
								}
								return ui.Icon(lucide.MessageCircle, 16, colors.Muted)
							}),
							base.Button{TestID: "sessions-open-" + id, LabelFunc: title, Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0), Gap: 2},
								OnPress: func(core.PressEvent) { row := item(); onOpen(cmp.Or(row.BotID, botID), row.ID, title()) }, Children: labelGroup()},
							core.Show{When: func() bool { return item().Busy }, Then: func(*core.Scope) core.Node {
								return core.Text{ContentFunc: func() string {
									r, tm := item(), now.Get()
									if r.ToolStartedAt > 0 && tm-r.ToolStartedAt > 30*time.Minute.Milliseconds() ||
										(r.ToolStartedAt == 0 && r.LastActivityAt > 0 && tm-r.LastActivityAt > 5*time.Minute.Milliseconds()) {
										return "Stalled"
									}
									return "Running"
								}, Style: core.Style{FontSize: 11.5, Padding: core.Symmetric(2, 8), CornerRadius: 4}, StyleFunc: ui.Filled(colors.Inset)}
							}},
							core.Show{When: func() bool { return item().kind() == "Session" && item().writable() }, Then: func(*core.Scope) core.Node {
								verb, prefix := "Archive", "sessions-archive-"
								if item().ArchivedAt != 0 {
									verb, prefix = "Restore", "sessions-restore-"
								}
								return ui.Button{Text: verb, TestID: prefix + id, Size: ui.SizeSmall, Variant: ui.ButtonGhost,
									Disabled: func() bool { return writing.Get() != 0 || loading.Get() }, OnPress: func() { changeArchive(item()) }}
							}},
							ui.Icon(lucide.ChevronRight, 16, colors.Muted),
						}}
				}),
			}}
	}

	return core.Scroll{TestID: "sessions-list", Style: ui.FillRest, Children: []core.Node{
		core.View{Style: core.Style{Width: core.Pct(100), MaxWidth: core.Pt(816), Padding: core.Edges{Top: 24, Left: 24, Right: 24, Bottom: 40}, Gap: 24}, Children: []core.Node{
			core.View{Style: core.Style{Gap: 6, MaxWidth: core.Pt(640)}, Children: []core.Node{
				core.Text{Content: "Sessions", Role: core.RoleHeader, Style: core.Style{FontSize: 18, FontWeight: core.FontWeightSemibold, LineHeight: 22.5}},
				core.Text{ContentFunc: func() string {
					return "Sessions of " + name.Get() + " apart from the main timeline. Each session has its own context. Sessions you open and child sessions that " + name.Get() + " starts take messages. Automation runs are read-only."
				},
					Style: core.Style{FontSize: 13.5, LineHeight: 22}, StyleFunc: ui.Colored(colors.Muted)},
			}},
			core.View{Style: core.Style{Gap: 16}, Children: []core.Node{
				core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, AlignItems: core.AlignCenter, Gap: 8}, Children: []core.Node{
					core.Text{Content: "All sessions", Role: core.RoleHeader, Style: core.Style{FontSize: 14, LineHeight: 21, FontWeight: core.FontWeightSemibold}},
					core.Text{ContentFunc: func() string {
						if len(active.Get()) == 0 {
							return ""
						}
						n := 0
						for _, row := range visible() {
							if row.Busy {
								n++
							}
						}
						return fmt.Sprintf("%d active · newest first", n)
					}, Style: core.Style{FlexGrow: 1, FontSize: 13}, StyleFunc: ui.Colored(colors.Muted)},
					core.Show{When: func() bool { return len(active.Get()) > 0 }, Then: func(*core.Scope) core.Node {
						return ui.Select[string]{TestID: "sessions-kind", Label: "Kind", Value: kind.Get, OnChange: kind.Set,
							Options: []ui.Option[string]{{Value: "All kinds", Label: "All kinds"}, {Value: "Session", Label: "Session"}, {Value: "Automation", Label: "Automation"}},
							Style:   core.Style{Width: core.Pt(107), Height: core.Pt(32), FontSize: 12.5, Margin: core.Edges{Top: -4, Bottom: -4}}}
					}},
				}},
				core.Show{When: func() bool { return loadError.Get() != "" }, Then: func(*core.Scope) core.Node {
					return core.View{Style: core.Style{Gap: 6}, Children: []core.Node{
						core.Text{ContentFunc: func() string { return "Could not load sessions. " + loadError.Get() }, Role: core.RoleAlert},
						ui.Button{Text: "Retry", TestID: "sessions-refresh", Variant: ui.ButtonGhost, Style: core.Style{AlignSelf: core.AlignStart}, OnPress: load},
					}}
				}, Else: func(*core.Scope) core.Node {
					return core.Show{When: loaded.Get, Then: func(*core.Scope) core.Node {
						return core.Show{When: func() bool { return len(visible()) > 0 }, Then: func(*core.Scope) core.Node { return list(visible) }, Else: func(*core.Scope) core.Node {
							return core.View{Style: core.Style{Gap: 4, Padding: core.All(16), CornerRadius: 8, BorderWidth: 1},
								StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} }, Children: []core.Node{
									core.Text{ContentFunc: func() string {
										if len(active.Get()) > 0 {
											return "No sessions of this kind"
										}
										return "No sessions yet"
									}, Style: core.Style{FontSize: 14, FontWeight: core.FontWeightMedium}},
									core.Text{ContentFunc: func() string {
										if len(active.Get()) > 0 {
											return "Choose All kinds to see every session."
										}
										return "Your sessions, child sessions that " + name.Get() + " starts, and automation runs show here. Use New session for work that needs a clean context."
									}, Style: core.Style{FontSize: 13, LineHeight: 21}, StyleFunc: ui.Colored(colors.Muted)},
								}}
						}}
					}, Else: func(*core.Scope) core.Node { return core.Text{Content: "Loading…", Role: core.RoleStatus} }}
				}},
			}},
			core.Show{When: func() bool { return len(archived()) > 0 }, Then: func(*core.Scope) core.Node {
				return core.View{Style: core.Style{Gap: 16, Borders: core.Edges{Top: 1}, Padding: core.Edges{Top: 24}}, StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} }, Children: []core.Node{
					core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, JustifyContent: core.JustifySpaceBetween}, Children: []core.Node{
						core.Text{Content: "Archived", Role: core.RoleHeader, Style: core.Style{FontSize: 14, FontWeight: core.FontWeightSemibold}},
						core.Show{When: archivedOpen.Get, Then: func(*core.Scope) core.Node {
							return ui.Button{Text: "Hide archived", TestID: "sessions-active", Variant: ui.ButtonGhost, OnPress: func() { archivedOpen.Set(false) }}
						}, Else: func(*core.Scope) core.Node {
							return ui.Button{Text: "Show archived", TestID: "sessions-archived", Variant: ui.ButtonGhost, OnPress: func() { archivedOpen.Set(true) }}
						}},
					}},
					core.Show{When: archivedOpen.Get, Then: func(*core.Scope) core.Node { return list(archived) }},
				}}
			}},
			core.Show{When: func() bool { return writeError.Get() != "" || status.Get() != "" }, Then: func(*core.Scope) core.Node {
				return core.Text{TestID: "sessions-status", Role: core.RoleStatus, ContentFunc: func() string { return cmp.Or(writeError.Get(), status.Get()) }}
			}},
		}},
	}}
}
