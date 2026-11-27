package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/base/textview"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

const (
	artifactsPreviewLimit  = 64 << 10
	artifactsImageLimit    = 8 << 20
	artifactsDownloadLimit = 32 << 20
	artifactsDescription   = "Files in this bot’s artifacts directory. All sessions in this bot show the same files."
)

type artifactsFile struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modifiedAt"`
}

func artifactsValidName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 && !strings.ContainsAny(name, "/\\\x00\r\n")
}

func artifactsCanPreview(file artifactsFile) bool {
	if !artifactsValidName(file.Name) || file.Size < 0 || file.Size > artifactsPreviewLimit {
		return false
	}
	switch strings.ToLower(path.Ext(file.Name)) {
	case ".txt", ".md", ".mdown", ".markdown", ".log", ".csv", ".tsv", ".json", ".yaml", ".yml":
		return true
	default:
		return false
	}
}

func artifactsMarkdown(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".mdown", ".markdown":
		return true
	}
	return false
}

func artifactsCanShowImage(file artifactsFile) bool {
	if !artifactsValidName(file.Name) || file.Size < 0 || file.Size > artifactsImageLimit {
		return false
	}
	switch strings.ToLower(path.Ext(file.Name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

func artifactsReadLimited(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("the file exceeds %d KiB", limit/1024)
	}
	return data, nil
}

func artifactsReadText(body io.Reader) (string, error) {
	data, err := artifactsReadLimited(body, artifactsPreviewLimit)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", errors.New("the file is not UTF-8 text")
	}
	text := string(data)
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return "", errors.New("the file contains control characters")
		}
	}
	return text, nil
}

func artifactsOpen(ctx context.Context, endpoint string, file artifactsFile) (*http.Response, error) {
	if !artifactsValidName(file.Name) {
		return nil, errors.New("invalid artifact name")
	}
	return gatewayRequest(ctx, http.MethodGet, endpoint+"/"+url.PathEscape(file.Name), "*/*", nil, "")
}

func artifactsFetchText(ctx context.Context, endpoint string, file artifactsFile) (string, error) {
	if !artifactsCanPreview(file) {
		return "", errors.New("this file cannot be previewed")
	}
	res, err := artifactsOpen(ctx, endpoint, file)
	if err != nil {
		return "", err
	}
	defer closeChatBody(ctx, res.Body)()
	mediaType, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	switch mediaType {
	case "application/octet-stream", "text/plain", "text/markdown":
		return artifactsReadText(res.Body)
	default:
		return "", errors.New("the response is not a text file")
	}
}

func artifactsPanel(cx *core.Scope, botID string, sessionID int, close func(), continueChat func(id int, draft string)) core.Node {
	colors := ui.ColorsOf(cx)
	return artifactsDialog(cx, "artifacts-panel", "Artifacts", artifactsDescription, close,
		artifactsContent(cx, botID, sessionID, close, continueChat),
		base.Button{TestID: "artifacts-close", Label: "Close", OnPress: func(core.PressEvent) { close() },
			HitSlop: core.Edges{Top: 6, Bottom: 6}, Style: core.Style{Width: core.Pt(57), Height: core.Pt(32),
				Direction: core.Row, AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter, CornerRadius: 6},
			StyleFunc: func() core.Style {
				return core.Style{Background: colors.Inset(), Hover: &core.Style{Background: colors.Hover()}, FocusVisible: ui.FocusRing(cx, 2)}
			}, Children: []core.Node{core.Text{Content: "Close", Style: core.Style{FontSize: 13, LineHeight: 21.125, FontWeight: core.FontWeightMedium}}}})
}

// A legacy artifacts route can still show its contents while the workspace maps it to Main.
func artifactsPage(cx *core.Scope, botID string) core.Node {
	return core.View{Style: ui.FillRest.Merge(core.Style{Padding: core.All(20), Gap: 12}), Children: []core.Node{
		core.Text{Content: "Artifacts", Role: core.RoleHeader, Style: core.Style{FontSize: 18, FontWeight: core.FontWeightSemibold}},
		core.Text{Content: artifactsDescription, StyleFunc: ui.Colored(ui.ColorsOf(cx).Muted)},
		artifactsContent(cx, botID, 0, nil, nil),
	}}
}

func artifactsDialog(cx *core.Scope, id, title, description string, close func(), body core.Node, footer ...core.Node) core.Node {
	colors := ui.ColorsOf(cx)
	border := func() core.Style { return core.Style{BorderColor: colors.Border()} }
	return base.Dialog{TestID: id, Label: title, AccessibilityDescription: description, DefaultOpen: true,
		OnOpenChange: func(open bool, _ base.Reason) {
			if !open && close != nil {
				close()
			}
		},
		BackdropStyle: core.Style{Background: core.RGBA(0, 0.1), BackdropFilters: []core.Filter{core.Blur(4)}},
		PopupStyleFunc: func() core.Style {
			window := cx.Env().Window()
			return core.Style{Width: core.Pt(min(520, window.Width)), MaxHeight: core.Pt(window.Height * .92), Gap: 16,
				MinHeight: core.Pt(0), CornerRadius: 12, Background: colors.Surface(), OutlineWidth: 1,
				OutlineColor: colors.Border(), Overflow: core.Hidden}
		}, Children: []core.Node{
			core.View{Style: core.Style{Padding: core.Edges{Top: 20, Right: 20, Bottom: 16, Left: 20}, Gap: 8, Borders: core.Edges{Bottom: 1}}, StyleFunc: border, Children: []core.Node{
				core.Text{Content: title, Role: core.RoleHeader, Style: core.Style{FontSize: 15, LineHeight: 24.375, FontWeight: core.FontWeightSemibold}},
				core.Text{Content: description, Style: core.Style{FontSize: 12.5, LineHeight: 20.3125}, StyleFunc: ui.Colored(colors.Muted)},
			}},
			body,
			core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, Gap: 8, Padding: core.Symmetric(12, 20), Borders: core.Edges{Top: 1}}, StyleFunc: func() core.Style {
				justify := core.JustifyEnd
				if cx.Env().Window().Width < 640 {
					justify = core.JustifyCenter
				}
				return core.Style{BorderColor: colors.Border(), JustifyContent: justify}
			}, Children: footer},
		}}
}

func artifactsContent(cx *core.Scope, botID string, sessionID int, close func(), continueChat func(id int, draft string)) core.Node {
	endpoint := botPath(botID) + "/artifacts"
	colors := ui.ColorsOf(cx)
	files := core.NewSignalFunc(cx, []artifactsFile(nil), slices.Equal)
	selection := core.NewSignal(cx, "")
	loaded := core.NewSignal(cx, false)
	loading := core.NewSignal(cx, false)
	failure := core.NewSignal(cx, "")
	mutating := core.NewSignal(cx, false)
	var revision int
	actionError := core.NewSignal(cx, "")
	editing := core.NewSignal(cx, "")
	newName := core.NewSignal(cx, "")
	confirming := core.NewSignal(cx, "")
	selected := core.NewMemo(cx, func() artifactsFile {
		if i := slices.IndexFunc(files.Get(), func(file artifactsFile) bool { return file.Name == selection.Get() }); i >= 0 {
			return files.Get()[i]
		}
		return artifactsFile{}
	})
	load := func() {
		if loading.Get() || mutating.Get() {
			return
		}
		loading.Set(true)
		requested := revision
		work(cx, func(ctx context.Context) ([]artifactsFile, error) {
			var result struct {
				Artifacts []artifactsFile `json:"artifacts"`
			}
			err := requestJSON(ctx, http.MethodGet, endpoint, nil, &result)
			return result.Artifacts, err
		}, func(result []artifactsFile, err error) {
			loading.Set(false)
			if requested != revision {
				return
			}
			if err != nil {
				failure.Set(err.Error())
				return
			}
			files.Set(result)
			loaded.Set(true)
			failure.Set("")
		})
	}
	cx.Post(func() {
		var poll func()
		poll = func() { load(); cx.AfterFunc(3*time.Second, poll) }
		poll()
	})
	write := func(name, next string, deleting bool) {
		if mutating.Get() || !artifactsValidName(name) || (!deleting && !artifactsValidName(next)) {
			return
		}
		mutating.Set(true)
		revision++
		actionError.Set("")
		cx.Post(func() {
			work(cx, func(ctx context.Context) (artifactsFile, error) {
				urlPath := endpoint + "/" + url.PathEscape(name)
				if deleting {
					return artifactsFile{}, requestJSON(ctx, http.MethodDelete, urlPath, nil, nil)
				}
				var saved artifactsFile
				err := requestJSON(ctx, http.MethodPatch, urlPath, struct {
					Name string `json:"name"`
				}{next}, &saved)
				return saved, err
			}, func(saved artifactsFile, err error) {
				mutating.Set(false)
				if err != nil {
					actionError.Set(err.Error())
					load()
					return
				}
				updated := slices.DeleteFunc(slices.Clone(files.Get()), func(file artifactsFile) bool { return file.Name == name || file.Name == saved.Name })
				if deleting {
					confirming.Set("")
					selection.Set("")
				} else {
					updated = append(updated, saved)
					editing.Set("")
					selection.Set(saved.Name)
				}
				files.Set(updated)
				load()
			})
		})
	}
	download := func(file artifactsFile) {
		if mutating.Get() {
			return
		}
		if !artifactsValidName(file.Name) || file.Size > artifactsDownloadLimit {
			actionError.Set("This file exceeds the 32 MiB download limit.")
			return
		}
		mutating.Set(true)
		actionError.Set("")
		work(cx, func(ctx context.Context) ([]byte, error) {
			res, err := artifactsOpen(ctx, endpoint, file)
			if err != nil {
				return nil, err
			}
			defer closeChatBody(ctx, res.Body)()
			return artifactsReadLimited(res.Body, artifactsDownloadLimit)
		}, func(data []byte, err error) {
			if err != nil {
				mutating.Set(false)
				actionError.Set("Could not download file. " + err.Error())
				return
			}
			cx.SaveFile(core.SaveOptions{Name: file.Name, Data: data}, func(_ bool, _ string, err error) {
				mutating.Set(false)
				if err != nil {
					actionError.Set("Could not save file. " + err.Error())
				}
			})
		})
	}
	resume := func(file artifactsFile) {
		if continueChat == nil || mutating.Get() || !artifactsValidName(file.Name) {
			return
		}
		mutating.Set(true)
		actionError.Set("")
		work(cx, func(ctx context.Context) (chatSession, error) {
			current, err := chatGetSession(ctx, botID, sessionID)
			if err != nil {
				return chatSession{}, err
			}
			if current.writable() {
				return current, nil
			}
			return newWorkspaceSession(ctx, botID, current.ID)
		}, func(target chatSession, err error) {
			mutating.Set(false)
			if err != nil {
				actionError.Set("Could not open a writable session. " + err.Error())
				return
			}
			id := target.ID
			if target.Main {
				id = 0
			}
			draft := "Continue editing the bot artifact " + file.Name + " at " + endpoint + "/" + url.PathEscape(file.Name) + ". This file is in this bot's artifacts directory, not the workspace source."
			if close != nil {
				close()
			}
			continueChat(id, draft)
		})
	}
	problem := func(testID string) core.Node {
		return core.Show{When: func() bool { return actionError.Get() != "" }, Then: func(*core.Scope) core.Node {
			return core.Text{TestID: testID, ContentFunc: actionError.Get, Role: core.RoleAlert, StyleFunc: ui.Colored(colors.Danger)}
		}}
	}
	return core.Scroll{TestID: "artifacts-list", Style: core.Style{FlexShrink: 1, MinHeight: core.Pt(0), Padding: core.Symmetric(16, 20), Gap: 16}, Children: []core.Node{
		core.Show{When: func() bool { return failure.Get() != "" }, Then: func(*core.Scope) core.Node {
			return core.View{Style: core.Style{Gap: 6}, Children: []core.Node{
				core.Text{TestID: "artifacts-status", ContentFunc: func() string { return "Could not load artifacts. " + failure.Get() }, Role: core.RoleAlert},
				ui.Button{Text: "Retry", TestID: "artifacts-refresh", Variant: ui.ButtonSecondary, Style: core.Style{AlignSelf: core.AlignStart}, OnPress: load},
			}}
		}, Else: func(*core.Scope) core.Node {
			return core.Show{When: loaded.Get, Then: func(*core.Scope) core.Node {
				return core.Show{When: func() bool { return len(files.Get()) > 0 }, Then: func(*core.Scope) core.Node {
					return core.View{Style: core.Style{Gap: 8}, Children: []core.Node{
						core.Each(files.Get, func(file artifactsFile) string { return file.Name }, func(_ *core.Scope, file func() artifactsFile, _ func() int) core.Node {
							name := file().Name
							return base.Button{TestID: "artifacts-open-" + name, Label: name, Disabled: mutating.Get, Selected: func() bool { return selection.Get() == name },
								OnPress: func(core.PressEvent) { actionError.Set(""); selection.Set(name) },
								Style:   core.Style{Padding: core.Symmetric(0, 10), BorderWidth: 1, CornerRadius: 6}, StyleFunc: func() core.Style {
									return core.Style{BorderColor: colors.Border(), Hover: &core.Style{Background: colors.Hover()}, Selected: &core.Style{Background: colors.Inset()}}
								}, Children: []core.Node{core.Text{Content: name, Style: core.Style{FontSize: 13, LineHeight: 20.5}}}}
						}),
					}}
				}, Else: func(*core.Scope) core.Node {
					return core.Text{Content: "No artifacts yet.", TestID: "artifacts-status", Role: core.RoleStatus, Style: core.Style{FontSize: 14, LineHeight: 20}, StyleFunc: ui.Colored(colors.Muted)}
				}}
			}, Else: func(*core.Scope) core.Node {
				return core.Text{Content: "Loading artifacts…", TestID: "artifacts-status", Role: core.RoleStatus}
			}}
		}},
		core.Show{When: func() bool { return selected.Get().Name != "" && failure.Get() == "" }, Then: func(*core.Scope) core.Node {
			return core.View{Style: core.Style{Gap: 12}, Children: []core.Node{
				core.Text{ContentFunc: func() string { return selected.Get().Name }, Role: core.RoleHeader, Style: core.Style{FontSize: 14, FontWeight: core.FontWeightSemibold}},
				core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, Gap: 8}, Children: []core.Node{
					ui.Button{Text: "Download", TestID: "artifacts-download", Variant: ui.ButtonGhost, Size: ui.SizeSmall, Disabled: mutating.Get, OnPress: func() { download(selected.Get()) }},
					core.Show{When: func() bool { return continueChat != nil }, Then: func(*core.Scope) core.Node {
						return ui.Button{Text: "Continue editing", TestID: "artifacts-continue", Variant: ui.ButtonSecondary, Size: ui.SizeSmall, Disabled: mutating.Get, OnPress: func() { resume(selected.Get()) }}
					}},
					ui.Button{Text: "Rename", TestID: "artifacts-rename", Variant: ui.ButtonSecondary, Size: ui.SizeSmall, Disabled: mutating.Get, OnPress: func() { actionError.Set(""); newName.Set(selected.Get().Name); editing.Set(selected.Get().Name) }},
					ui.Button{Text: "Delete", TestID: "artifacts-delete", Variant: ui.ButtonDanger, Size: ui.SizeSmall, Disabled: mutating.Get, OnPress: func() { actionError.Set(""); confirming.Set(selected.Get().Name) }},
				}},
				core.Show{When: func() bool { return editing.Get() == "" && confirming.Get() == "" }, Then: func(*core.Scope) core.Node { return problem("artifacts-action-error") }},
				core.Dynamic(func(previewCX *core.Scope) core.Node {
					file := selected.Get()
					return artifactsPreview(previewCX, endpoint, file)
				}),
			}}
		}},
		core.Show{When: func() bool { return editing.Get() != "" }, Then: func(dialogCX *core.Scope) core.Node {
			cancel := func() { editing.Set(""); actionError.Set("") }
			return artifactsDialog(dialogCX, "artifacts-rename-dialog", "Rename artifact", "Rename this bot file. Existing file names cannot be replaced.", cancel,
				core.View{Style: core.Style{Padding: core.Symmetric(16, 20), Gap: 8}, Children: []core.Node{
					core.Text{Content: "File name"},
					ui.Input{TestID: "artifacts-name", Label: "File name", Value: newName.Get, OnChange: newName.Set, Disabled: mutating.Get, AutoFocus: true},
					problem("artifacts-rename-error"),
				}},
				ui.Button{Text: "Cancel", Variant: ui.ButtonSecondary, OnPress: cancel, Disabled: mutating.Get},
				ui.Button{Text: "Save", TestID: "artifacts-save-name", Disabled: func() bool { return mutating.Get() || !artifactsValidName(strings.TrimSpace(newName.Get())) }, OnPress: func() { write(editing.Get(), strings.TrimSpace(newName.Get()), false) }})
		}},
		core.Show{When: func() bool { return confirming.Get() != "" }, Then: func(dialogCX *core.Scope) core.Node {
			cancel := func() { confirming.Set(""); actionError.Set("") }
			return artifactsDialog(dialogCX, "artifacts-delete-dialog", "Delete artifact", "Delete "+confirming.Get()+"? Workspace source files are unchanged.", cancel,
				core.View{Style: core.Style{Padding: core.Symmetric(8, 20)}, Children: []core.Node{problem("artifacts-delete-error")}},
				ui.Button{Text: "Cancel", Variant: ui.ButtonSecondary, OnPress: cancel, Disabled: mutating.Get},
				ui.Button{Text: "Delete", TestID: "artifacts-delete-confirm", Variant: ui.ButtonDanger, Disabled: mutating.Get, OnPress: func() { write(confirming.Get(), "", true) }})
		}},
	}}
}

func artifactsPreview(cx *core.Scope, endpoint string, file artifactsFile) core.Node {
	isText := artifactsCanPreview(file)
	if !isText && !artifactsCanShowImage(file) {
		return core.Text{Content: "Download this file to open it.", StyleFunc: ui.Colored(ui.ColorsOf(cx).Muted)}
	}
	type preview struct {
		text string
		data []byte
	}
	result := core.NewSignal(cx, (*preview)(nil))
	failure := core.NewSignal(cx, "")
	cx.Post(func() {
		work(cx, func(ctx context.Context) (preview, error) {
			if isText {
				text, err := artifactsFetchText(ctx, endpoint, file)
				return preview{text: text}, err
			}
			res, err := artifactsOpen(ctx, endpoint, file)
			if err != nil {
				return preview{}, err
			}
			defer closeChatBody(ctx, res.Body)()
			mediaType, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
			switch mediaType {
			case "image/png", "image/jpeg", "image/gif", "image/webp":
			default:
				return preview{}, errors.New("the response is not a supported image")
			}
			data, err := artifactsReadLimited(res.Body, artifactsImageLimit)
			return preview{data: data}, err
		}, func(value preview, err error) {
			if err != nil {
				failure.Set("Could not load file. " + err.Error())
				return
			}
			result.Set(new(value))
		})
	})
	return core.Dynamic(func(*core.Scope) core.Node {
		if failure.Get() != "" {
			return core.Text{ContentFunc: failure.Get, Role: core.RoleAlert}
		}
		value := result.Get()
		if value == nil {
			return core.Text{Content: "Loading file…", Role: core.RoleStatus}
		}
		if !isText {
			return core.Image{TestID: "artifacts-content", Source: core.ImageSource{Data: value.data}, Label: file.Name,
				Style: core.Style{Width: core.Pct(100), Height: core.Pt(240)}, OnError: func(error) { failure.Set("Could not show this image. Download it to open it.") }}
		}
		if value.text == "" {
			return core.Text{Content: "This file is empty."}
		}
		if artifactsMarkdown(file.Name) {
			return ui.TextView{TestID: "artifacts-content", Markdown: value.text, Selectable: true,
				RenderImage: func(_ *core.Scope, img textview.Image) core.Node { return core.Text{Content: img.Alt} }}
		}
		return core.Text{TestID: "artifacts-content", Content: value.text, Selectable: core.SelectableOn, Style: core.Style{FontSize: 14, LineHeight: 22}}
	})
}
