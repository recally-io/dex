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
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

const artifactsPreviewLimit = 64 << 10

type artifactsFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

func artifactsCanPreview(file artifactsFile) bool {
	if strings.ContainsAny(file.Name, "/\\\x00") || file.Size < 0 || file.Size > artifactsPreviewLimit {
		return false
	}
	switch strings.ToLower(path.Ext(file.Name)) {
	case ".txt", ".md", ".mdown", ".markdown", ".log", ".csv", ".tsv", ".json", ".yaml", ".yml":
		return true
	default:
		return false
	}
}

func artifactsReadText(body io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(body, artifactsPreviewLimit+1))
	if err != nil {
		return "", err
	}
	if len(data) > artifactsPreviewLimit {
		return "", errors.New("文件超过 64 KiB，无法预览")
	}
	if !utf8.Valid(data) {
		return "", errors.New("文件不是 UTF-8 文本")
	}
	text := string(data)
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return "", errors.New("文件包含无法预览的控制字符")
		}
	}
	return text, nil
}

func artifactsFetchText(ctx context.Context, endpoint string, file artifactsFile) (string, error) {
	if !artifactsCanPreview(file) {
		return "", errors.New("此文件无法预览")
	}
	res, err := gatewayRequest(ctx, http.MethodGet, endpoint+"/"+url.PathEscape(file.Name), "*/*", nil, "")
	if err != nil {
		return "", err
	}
	defer closeChatBody(ctx, res.Body)()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("无法读取文件（HTTP %d）", res.StatusCode)
	}
	mediaType, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	// Dex serves non-image artifacts as attachments with application/octet-stream.
	switch mediaType {
	case "application/octet-stream", "text/plain", "text/markdown":
		return artifactsReadText(res.Body)
	default:
		return "", errors.New("响应不是可预览的文件")
	}
}

func artifactsPage(cx *core.Scope, botID string) core.Node {
	endpoint := botPath(botID) + "/artifacts"
	files := core.NewSignalFunc(cx, []artifactsFile(nil), nil)
	selected := core.NewSignal(cx, (*artifactsFile)(nil))
	loading := core.NewSignal(cx, false)
	status := core.NewSignal(cx, "正在加载文件…")
	load := func() {
		if loading.Get() {
			return
		}
		loading.Set(true)
		status.Set("正在加载文件…")
		work(cx, func(ctx context.Context) ([]artifactsFile, error) {
			var result struct {
				Artifacts []artifactsFile `json:"artifacts"`
			}
			err := requestJSON(ctx, http.MethodGet, endpoint, nil, &result)
			return result.Artifacts, err
		}, func(result []artifactsFile, err error) {
			loading.Set(false)
			if err != nil {
				status.Set("加载文件失败，可刷新重试：" + err.Error())
				return
			}
			files.Set(result)
			if len(result) == 0 {
				status.Set("暂无文件。Bot 生成文件后，可刷新查看。")
			} else {
				status.Set("")
			}
		})
	}
	cx.Post(load)

	colors := ui.ColorsOf(cx)
	return core.View{Style: ui.FillRest, Children: []core.Node{
		core.Show{When: func() bool { return selected.Get() == nil }, Then: func(*core.Scope) core.Node {
			return core.Scroll{TestID: "artifacts-list", Style: ui.FillRest.Merge(core.Style{Padding: core.All(12), Gap: 12}), Children: []core.Node{
				core.Text{Content: "文件", Role: core.RoleHeader, Style: core.Style{FontSize: 22, FontWeight: core.FontWeightSemibold}},
				ui.Button{Text: "刷新文件", TestID: "artifacts-refresh", Variant: ui.ButtonSecondary, Busy: loading.Get, OnPress: load},
				core.Show{When: func() bool { return status.Get() != "" }, Then: func(*core.Scope) core.Node {
					return core.Text{TestID: "artifacts-status", ContentFunc: status.Get, Role: core.RoleStatus}
				}},
				core.Each(files.Get, func(file artifactsFile) string { return file.Name }, func(_ *core.Scope, file func() artifactsFile, _ func() int) core.Node {
					return core.View{Style: core.Style{Gap: 6, Padding: core.All(10), BorderWidth: 1, CornerRadius: 8},
						StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} }, Children: []core.Node{
							core.Text{Content: file().Name, Role: core.RoleHeader, Style: core.Style{FontWeight: core.FontWeightSemibold}},
							core.Text{ContentFunc: func() string { return fmt.Sprintf("%d 字节", file().Size) },
								StyleFunc: func() core.Style { return core.Style{Color: colors.Muted()} }},
							core.Show{When: func() bool { return artifactsCanPreview(file()) }, Then: func(*core.Scope) core.Node {
								return ui.Button{Text: "预览", Label: "预览 " + file().Name, TestID: "artifacts-open-" + file().Name,
									Variant: ui.ButtonSecondary, OnPress: func() { selected.Set(new(file())) }}
							}, Else: func(*core.Scope) core.Node {
								return core.Text{ContentFunc: func() string {
									if file().Size > artifactsPreviewLimit {
										return "超过 64 KiB，无法预览"
									}
									return "此格式不支持预览"
								}}
							}},
						}}
				}),
			}}
		}, Else: func(detail *core.Scope) core.Node {
			return artifactsPreview(detail, endpoint, *selected.Get(), func() { selected.Set(nil) })
		}},
	}}
}

func artifactsPreview(cx *core.Scope, endpoint string, file artifactsFile, back func()) core.Node {
	loading := core.NewSignal(cx, false)
	status := core.NewSignal(cx, "正在加载预览…")
	failed := core.NewSignal(cx, false)
	text := core.NewSignal(cx, "")
	load := func() {
		if loading.Get() {
			return
		}
		loading.Set(true)
		status.Set("正在加载预览…")
		work(cx, func(ctx context.Context) (string, error) {
			return artifactsFetchText(ctx, endpoint, file)
		}, func(result string, err error) {
			loading.Set(false)
			failed.Set(err != nil)
			if err != nil {
				status.Set("预览失败：" + err.Error())
				return
			}
			text.Set(result)
			if result == "" {
				status.Set("此文件为空。")
			} else {
				status.Set("")
			}
		})
	}
	cx.Post(load)
	return core.Scroll{TestID: "artifacts-preview", Style: ui.FillRest.Merge(core.Style{Padding: core.All(12), Gap: 12}), Children: []core.Node{
		ui.Button{Text: "返回文件列表", TestID: "artifacts-back", Variant: ui.ButtonSecondary, OnPress: back},
		core.Text{Content: file.Name, Role: core.RoleHeader, Style: core.Style{FontSize: 20, FontWeight: core.FontWeightSemibold}},
		core.Text{Content: "纯文本预览"},
		core.Show{When: func() bool { return status.Get() != "" }, Then: func(*core.Scope) core.Node {
			return core.Text{TestID: "artifacts-preview-status", ContentFunc: status.Get, Role: core.RoleStatus}
		}},
		core.Show{When: failed.Get, Then: func(*core.Scope) core.Node {
			return ui.Button{Text: "重试预览", TestID: "artifacts-retry", Variant: ui.ButtonSecondary, Busy: loading.Get, OnPress: load}
		}},
		core.Show{When: func() bool { return text.Get() != "" }, Then: func(*core.Scope) core.Node {
			return core.Text{TestID: "artifacts-content", ContentFunc: text.Get, Selectable: core.SelectableOn,
				Style: core.Style{FontSize: 15, MinWidth: core.Pt(0)}}
		}},
	}}
}
