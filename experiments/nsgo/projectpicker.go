package main

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

type projectDirectoryEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Git  bool   `json:"git"`
}

type projectDirectory struct {
	Path      string                  `json:"path"`
	Parent    string                  `json:"parent"`
	Home      string                  `json:"home"`
	Truncated bool                    `json:"truncated"`
	Entries   []projectDirectoryEntry `json:"entries"`
}

func saveProjectDirectory(ctx context.Context, botID string, id int, path string) (chatSession, error) {
	var saved chatSession
	body := struct {
		Cwd string `json:"cwd"`
	}{Cwd: path}
	err := requestJSON(ctx, http.MethodPut, chatSessionPath(botID, id)+"/project", body, &saved)
	if err == nil && saved.ID != id {
		err = errors.New("the response does not confirm the session")
	}
	return saved, err
}

func newSessionProject(cx *core.Scope, botID string, session chatSession, close func(), onSaved func(chatSession)) core.Node {
	const prefix = "new-session-project"
	saving := core.NewSignal(cx, "")
	picker := newProjectPicker(cx, botID, session, prefix, saving, func(saved chatSession) {
		onSaved(saved)
		close()
	})
	cancel := func() {
		if saving.Get() == "" {
			close()
		}
	}
	return ui.Dialog{
		TestID: prefix + "-dialog", Title: "Project directory",
		Description: "Choose a folder in the bot's Runner. Changes apply from the next message. Existing context stays unchanged.",
		Open:        func() bool { return true },
		OnOpenChange: func(open bool, _ base.Reason) {
			if !open {
				cancel()
			}
		},
		Children: []core.Node{core.Scroll{
			Style: core.Style{MinHeight: core.Pt(0), FlexShrink: 1},
			StyleFunc: func() core.Style {
				env := cx.Env()
				return core.Style{MaxHeight: core.Pt(max(80, min(440, (env.Window().Height-env.KeyboardHeight())*0.60)))}
			},
			Children: []core.Node{picker.body},
		}, picker.saveError},
		Footer: []core.Node{
			picker.clear,
			ui.Button{Text: "Cancel", TestID: prefix + "-cancel", Variant: ui.ButtonOutline,
				Disabled: func() bool { return saving.Get() != "" }, OnPress: cancel},
			picker.use,
		},
	}
}

type projectPicker struct {
	body, saveError, clear, use core.Node
}

func newProjectPicker(cx *core.Scope, botID string, initial chatSession, prefix string, saving *core.Signal[string], onSaved func(chatSession)) projectPicker {
	session := core.NewSignal(cx, chatSession{})
	sessionError := core.NewSignal(cx, "")
	draft := core.NewSignal(cx, cmp.Or(initial.Cwd, "~"))
	listing := core.NewSignal(cx, (*projectDirectory)(nil))
	loading := core.NewSignal(cx, false)
	directoryError := core.NewSignal(cx, "")
	recent := core.NewSignalFunc(cx, []string(nil), nil)
	recentStatus := core.NewSignal(cx, "Loading recent projects…")
	recentError := core.NewSignal(cx, "")
	saveError := core.NewSignal(cx, "")

	load := func(path string) {
		current := session.Get()
		if current.ID <= 0 || path == "" || loading.Get() || saving.Get() != "" {
			return
		}
		draft.Set(path)
		listing.Set(nil)
		loading.Set(true)
		directoryError.Set("")
		query := projectRunnerQuery(current.RunnerID)
		query.Set("botId", botID)
		query.Set("path", path)
		work(cx, func(ctx context.Context) (projectDirectory, error) {
			var result projectDirectory
			err := requestJSON(ctx, http.MethodGet, "/api/directories?"+query.Encode(), nil, &result)
			return result, err
		}, func(result projectDirectory, err error) {
			loading.Set(false)
			if err != nil {
				directoryError.Set("Cannot open directory: " + err.Error())
				return
			}
			listing.Set(new(result))
			if draft.Get() == path {
				draft.Set(result.Path)
			}
		})
	}
	readSession := func() {
		sessionError.Set("")
		work(cx, func(ctx context.Context) (chatSession, error) {
			return chatGetSession(ctx, botID, initial.ID)
		}, func(current chatSession, err error) {
			if err != nil {
				sessionError.Set("Cannot read session: " + err.Error())
				return
			}
			session.Set(current)
			load(cmp.Or(current.Cwd, "~"))
			endpoint := botPath(botID) + "/projects"
			if query := projectRunnerQuery(current.RunnerID).Encode(); query != "" {
				endpoint += "?" + query
			}
			work(cx, func(ctx context.Context) ([]string, error) {
				var result struct {
					Projects []string `json:"projects"`
				}
				err := requestJSON(ctx, http.MethodGet, endpoint, nil, &result)
				return result.Projects, err
			}, func(paths []string, err error) {
				recentStatus.Set("")
				if err != nil {
					recentError.Set("Cannot read recent projects: " + err.Error())
					return
				}
				recent.Set(paths)
				if len(paths) == 0 {
					recentStatus.Set("No recent projects")
				}
			})
		})
	}
	cx.Post(readSession)

	locked := func() bool { return saving.Get() != "" || !session.Get().writable() }
	save := func(path string) {
		if locked() {
			return
		}
		saving.Set("project")
		saveError.Set("")
		work(cx, func(ctx context.Context) (chatSession, error) {
			return saveProjectDirectory(ctx, botID, initial.ID, path)
		}, func(saved chatSession, err error) {
			saving.Set("")
			if err != nil {
				saveError.Set("Cannot confirm project save: " + err.Error())
				return
			}
			onSaved(saved)
		})
	}
	cannotUse := func() bool {
		data := listing.Get()
		return locked() || data == nil || strings.TrimSpace(draft.Get()) != data.Path
	}
	browse := func() { load(strings.TrimSpace(draft.Get())) }
	directoryStatus := func() string {
		if loading.Get() {
			return "Loading directories…"
		}
		if data := listing.Get(); data != nil {
			if data.Truncated {
				return "Showing the first 500 folders. Enter a path to open another folder."
			}
			if len(data.Entries) == 0 {
				return "No subdirectories"
			}
		}
		return ""
	}
	colors := ui.ColorsOf(cx)
	message := func(id string, role core.Role, text func() string) core.Node {
		color := colors.Muted
		if role == core.RoleAlert {
			color = colors.Danger
		}
		return core.Show{When: func() bool { return text() != "" }, Then: func(*core.Scope) core.Node {
			return core.Text{TestID: prefix + "-" + id, Role: role, ContentFunc: text, Style: ui.TypeBody, StyleFunc: ui.Colored(color)}
		}}
	}
	return projectPicker{
		body: core.View{Style: core.Style{Gap: 8}, Children: []core.Node{
			message("session-error", core.RoleAlert, sessionError.Get),
			core.Show{When: func() bool { return sessionError.Get() != "" }, Then: func(*core.Scope) core.Node {
				return ui.Button{Text: "Retry session", Variant: ui.ButtonSecondary, OnPress: readSession}
			}},
			message("session-status", core.RoleStatus, func() string {
				s := session.Get()
				if s.ID == 0 && sessionError.Get() == "" {
					return "Loading session…"
				}
				if s.ID > 0 && !s.writable() {
					return "This session is read-only."
				}
				return ""
			}),
			core.Show{When: func() bool { return session.Get().ID > 0 }, Then: func(*core.Scope) core.Node {
				return core.View{Style: core.Style{Gap: 8}, Children: []core.Node{
					core.Text{Content: "Recent", Role: core.RoleHeader},
					message("recent-status", core.RoleStatus, recentStatus.Get),
					message("recent-error", core.RoleAlert, recentError.Get),
					core.Show{When: func() bool { return len(recent.Get()) != 0 }, Then: func(*core.Scope) core.Node {
						return core.Scroll{Style: core.Style{MaxHeight: core.Pt(120)}, Children: []core.Node{
							core.Each(recent.Get, func(path string) string { return path }, func(_ *core.Scope, path func() string, _ func() int) core.Node {
								return ui.Button{TextFunc: path, Icon: lucide.Folder, Variant: ui.ButtonGhost,
									TextStyle: core.Style{TextOverflow: core.EllipsizeHead}, Disabled: locked, OnPress: func() { save(path()) }}
							}),
						}}
					}},
					core.View{Style: core.Style{Direction: core.Row, Gap: 8, AlignItems: core.AlignCenter}, Children: []core.Node{
						ui.Input{Label: "Directory path", TestID: prefix + "-path", Value: draft.Get, OnChange: draft.Set,
							Style:    core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0)},
							OnSubmit: func(string) { browse() }, ReadonlyFunc: func() bool { return saving.Get() != "" }},
						ui.Button{Text: "Browse", TestID: prefix + "-browse", Variant: ui.ButtonOutline, Busy: loading.Get,
							Disabled: func() bool { return strings.TrimSpace(draft.Get()) == "" || saving.Get() != "" }, OnPress: browse},
						ui.Button{Label: "Up", Icon: lucide.ArrowUp, TestID: prefix + "-up", Variant: ui.ButtonOutline,
							Disabled: func() bool {
								data := listing.Get()
								return saving.Get() != "" || data == nil || data.Parent == ""
							}, OnPress: func() {
								if data := listing.Get(); data != nil {
									load(data.Parent)
								}
							}},
						ui.Button{Label: "Home", Icon: lucide.House, TestID: prefix + "-home", Variant: ui.ButtonOutline,
							Disabled: func() bool { return loading.Get() || saving.Get() != "" }, OnPress: func() {
								home := "~"
								if data := listing.Get(); data != nil {
									home = cmp.Or(data.Home, home)
								}
								load(home)
							}},
					}},
					message("directory-status", core.RoleStatus, directoryStatus),
					message("directory-error", core.RoleAlert, directoryError.Get),
					core.Each(func() []projectDirectoryEntry {
						if data := listing.Get(); data != nil {
							return data.Entries
						}
						return nil
					}, func(entry projectDirectoryEntry) string { return entry.Path },
						func(_ *core.Scope, entry func() projectDirectoryEntry, _ func() int) core.Node {
							return ui.Button{TextFunc: func() string { return entry().Name }, Variant: ui.ButtonGhost,
								Icon: func() *core.SVG {
									if entry().Git {
										return lucide.GitBranch()
									}
									return lucide.Folder()
								}, LabelFunc: func() string {
									if dir := entry(); dir.Git {
										return "Git repository: " + dir.Name
									}
									return entry().Name
								}, Disabled: func() bool { return saving.Get() != "" }, OnPress: func() { load(entry().Path) }}
						}),
				}}
			}},
		}},
		saveError: message("save-error", core.RoleAlert, saveError.Get),
		clear: ui.Button{Text: "Clear project", TestID: prefix + "-reset", Variant: ui.ButtonOutline,
			Disabled: locked, OnPress: func() { save("") }},
		use: ui.Button{Text: "Use this folder", TestID: prefix + "-save", BusyLabel: "Saving…", Busy: func() bool { return saving.Get() == "project" },
			Disabled: cannotUse, OnPress: func() {
				if !cannotUse() {
					save(listing.Get().Path)
				}
			}},
	}
}

func projectRunnerQuery(runnerID string) url.Values {
	query := url.Values{}
	if runnerID != "" {
		query.Set("runnerId", runnerID)
	}
	return query
}
