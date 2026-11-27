package main

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

// Keep this limit equal to MessageInput.attachments in the API.
const composerMaxFiles = 10

type composerFile struct {
	key                   int
	name, uploadID, issue string
	size                  int64
}

type composerFiles struct {
	cx       *core.Scope
	lifetime context.Context
	path     string
	locked   func() bool
	files    *core.Signal[[]composerFile]
	picking  *core.Signal[bool]
	notice   *core.Signal[string]

	// The UI goroutine owns the queue. A worker owns the file it takes from it.
	queued map[int]core.File
	next   int
	active int
	cancel context.CancelFunc
}

func newComposerFiles(cx *core.Scope, botID string, blocked func() bool) *composerFiles {
	f := &composerFiles{
		cx: cx, lifetime: cx.Context(), path: botPath(botID) + "/uploads", locked: blocked,
		files: core.NewSignalFunc(cx, []composerFile(nil), slices.Equal), picking: core.NewSignal(cx, false),
		notice: core.NewSignal(cx, ""), queued: make(map[int]core.File),
	}
	cx.Cleanup(func() {
		for _, file := range f.queued {
			composerFileRelease(file)
		}
		clear(f.queued)
	})
	return f
}

// Any reports whether the draft contains a file, including an upload that failed.
func (f *composerFiles) Any() bool { return len(f.files.Get()) != 0 }

// Blocked reports whether the attachment draft prevents sending.
func (f *composerFiles) Blocked() bool {
	return f.picking.Get() || slices.ContainsFunc(f.files.Get(), func(file composerFile) bool { return file.uploadID == "" })
}

// IDs returns the ready upload IDs in picker order. Check Blocked before sending.
func (f *composerFiles) IDs() []string {
	var ids []string
	for _, file := range f.files.Get() {
		if file.uploadID != "" {
			ids = append(ids, file.uploadID)
		}
	}
	return ids
}

// Sent removes only the attachments from the confirmed message.
func (f *composerFiles) Sent(ids []string) {
	if len(ids) == 0 {
		return
	}
	remaining := slices.Clone(f.files.Get())
	f.files.Set(slices.DeleteFunc(remaining, func(file composerFile) bool {
		return file.uploadID != "" && slices.Contains(ids, file.uploadID)
	}))
	f.notice.Set("")
}

func (f *composerFiles) editsLocked() bool { return f.locked != nil && f.locked() }

func (f *composerFiles) pickerDisabled() bool {
	return f.editsLocked() || f.picking.Get() || len(f.files.Get()) >= composerMaxFiles
}

func (f *composerFiles) pick() {
	if f.pickerDisabled() {
		return
	}
	f.picking.Set(true)
	f.cx.PickFiles(core.PickFilesOptions{Multiple: true, Limit: composerMaxFiles + 1, Title: "添加附件", Button: "添加"}, func(picked []core.File, err error) {
		// Some platforms answer after the page that opened the picker has closed.
		if f.lifetime.Err() != nil {
			for _, file := range picked {
				composerFileRelease(file)
			}
			return
		}
		f.picking.Set(false)
		if f.editsLocked() || err != nil {
			for _, file := range picked {
				composerFileRelease(file)
			}
			if err != nil {
				f.notice.Set("无法选择附件。请重试。")
			}
			return
		}
		if len(picked) > 0 {
			f.stage(picked)
			f.pump()
		}
	})
}

func (f *composerFiles) stage(picked []core.File) {
	items := slices.Clone(f.files.Get())
	room := composerMaxFiles - len(items)
	if len(picked) > room {
		f.notice.Set("每条消息最多添加 10 个附件，超出的文件未添加。")
		for _, file := range picked[room:] {
			composerFileRelease(file)
		}
		picked = picked[:room]
	} else if len(picked) == room {
		f.notice.Set("每条消息最多添加 10 个附件。")
	} else {
		f.notice.Set("")
	}
	for _, file := range picked {
		f.next++
		item := composerFile{key: f.next, name: file.Name, size: file.Size}
		switch {
		case !composerFileNameOK(file.Name):
			item.name, item.issue = "附件", "文件名无效，请移除。"
		case file.Size > composerMaxFileSize:
			item.issue = composerFileTooLarge.Error()
		}
		if item.issue != "" {
			composerFileRelease(file)
		} else {
			f.queued[item.key] = file
		}
		items = append(items, item)
	}
	f.files.Set(items)
}

func (f *composerFiles) pump() {
	if f.active != 0 {
		return
	}
	for _, item := range f.files.Get() {
		file, waiting := f.queued[item.key]
		if !waiting {
			continue
		}
		delete(f.queued, item.key)
		// Upload one file at a time to bound the buffered body size on WebAssembly.
		ctx, cancel := context.WithTimeout(f.lifetime, 2*time.Minute)
		f.active, f.cancel = item.key, cancel
		go func() {
			defer cancel()
			id, issue := composerFileUpload(ctx, f.path, file)
			composerFileRelease(file)
			f.cx.Post(func() {
				f.active, f.cancel = 0, nil
				f.update(item.key, id, issue)
				f.pump()
			})
		}()
		return
	}
}

func (f *composerFiles) update(key int, id, issue string) {
	current := f.files.Get()
	if i := slices.IndexFunc(current, func(file composerFile) bool { return file.key == key }); i >= 0 {
		next := slices.Clone(current)
		next[i].uploadID, next[i].issue = id, issue
		f.files.Set(next)
	}
}

func (f *composerFiles) remove(key int) {
	if f.editsLocked() {
		return
	}
	if key == f.active {
		f.cancel()
	}
	if file, exists := f.queued[key]; exists {
		composerFileRelease(file)
		delete(f.queued, key)
	}
	f.notice.Set("")
	f.files.Set(slices.DeleteFunc(slices.Clone(f.files.Get()), func(file composerFile) bool { return file.key == key }))
}

func composerFileRelease(file core.File) {
	if file.Temporary && file.Path != "" {
		_ = os.Remove(file.Path)
	}
}

func (f *composerFiles) Button() core.Node {
	return ui.Button{TestID: "composer-files-add", Icon: lucide.Plus, LabelFunc: func() string {
		if len(f.files.Get()) >= composerMaxFiles {
			return "已达 10 个附件上限"
		}
		return "添加附件"
	}, Variant: ui.ButtonGhost, Disabled: f.pickerDisabled, OnPress: f.pick, Side: 36, IconSize: 16}
}

func (f *composerFiles) Chips() core.Node {
	return core.Show{When: func() bool { return f.Any() || f.notice.Get() != "" }, Then: func(cx *core.Scope) core.Node {
		colors := ui.ColorsOf(cx)
		return core.View{Style: core.Style{Gap: 4, MinWidth: core.Pt(0)}, Children: []core.Node{
			core.Show{When: f.Any, Then: func(*core.Scope) core.Node {
				return core.Scroll{TestID: "composer-files", Horizontal: true, Label: "待发送附件", NoShrink: true,
					Style: core.Style{Height: core.Pt(46), MinWidth: core.Pt(0)},
					Children: []core.Node{core.View{Style: core.Style{Direction: core.Row, Gap: 8}, Children: []core.Node{
						core.Each(f.files.Get, func(file composerFile) int { return file.key }, f.chip),
					}}},
				}
			}},
			core.Show{When: func() bool { return f.notice.Get() != "" }, Then: func(*core.Scope) core.Node {
				return core.Text{TestID: "composer-files-notice", Role: core.RoleStatus, ContentFunc: f.notice.Get,
					StyleFunc: func() core.Style { return core.Style{FontSize: 12, Color: colors.Muted()} }}
			}},
		}}
	}}
}

func (f *composerFiles) chip(cx *core.Scope, file func() composerFile, _ func() int) core.Node {
	colors := ui.ColorsOf(cx)
	key := file().key
	id := strconv.Itoa(key)
	return core.View{TestID: "composer-file-" + id,
		Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 4, Padding: core.Symmetric(2, 8),
			CornerRadius: 4, BorderWidth: 1, FlexShrink: 0, Width: core.Pt(196)},
		StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} },
		Children: []core.Node{
			core.View{Style: core.Style{FlexGrow: 1, MinWidth: core.Pt(0)}, Children: []core.Node{
				core.Text{ContentFunc: func() string { return file().name }, MaxLines: 1, Style: core.Style{FontSize: 13, FontWeight: core.FontWeightMedium}},
				core.Text{TestID: "composer-file-status-" + id, Role: core.RoleStatus,
					ContentFunc: func() string { return composerFileLabel(file()) }, MaxLines: 1,
					StyleFunc: func() core.Style {
						if file().issue != "" {
							return core.Style{FontSize: 12, Color: colors.Danger()}
						}
						return core.Style{FontSize: 12, Color: colors.Muted()}
					}},
			}},
			ui.Button{TestID: "composer-file-remove-" + id, Icon: lucide.X,
				LabelFunc: func() string { return "移除 " + file().name }, Variant: ui.ButtonGhost,
				Side: 36, IconSize: 14, Disabled: f.editsLocked, OnPress: func() { f.remove(key) }},
		},
	}
}

func composerFileLabel(file composerFile) string {
	switch {
	case file.issue != "":
		return file.issue
	case file.uploadID == "":
		return "上传中…"
	case file.size < 1024:
		return fmt.Sprintf("%d B", file.size)
	case file.size < 1024*1024:
		return fmt.Sprintf("%.0f KiB", float64(file.size)/1024)
	default:
		return fmt.Sprintf("%.1f MiB", float64(file.size)/(1024*1024))
	}
}
