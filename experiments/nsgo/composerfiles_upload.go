package main

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"unicode"

	"github.com/nsgo-dev/nsgo/core"
)

// Keep this limit equal to internal/upload.MaxSize.
const composerMaxFileSize = 20 << 20

var composerFileTooLarge = errors.New("超过 20 MiB，请移除。")

func composerFileNameOK(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 &&
		!strings.ContainsAny(name, `/\`) && !strings.ContainsFunc(name, unicode.IsControl)
}

func composerFileUpload(ctx context.Context, path string, file core.File) (id, issue string) {
	body, contentType, err := composerFileMultipart(ctx, file)
	if err != nil {
		if ctx.Err() != nil {
			return "", "上传超时，请移除。"
		}
		return "", err.Error()
	}
	res, err := gatewayRequest(ctx, http.MethodPost, path, "application/json", body, contentType)
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return "", "上传超时，请移除。"
		case errors.Is(err, errSignIn):
			return "", "请登录后重新添加。"
		default:
			return "", "上传失败，请移除。"
		}
	}
	defer closeChatBody(ctx, res.Body)()
	var reply struct {
		ID string `json:"id"`
	}
	if err := json.UnmarshalRead(res.Body, &reply); err != nil || reply.ID == "" {
		return "", "无法确认上传，请移除。"
	}
	return reply.ID, ""
}

func composerFileMultipart(ctx context.Context, file core.File) (*bytes.Buffer, string, error) {
	if !composerFileNameOK(file.Name) {
		return nil, "", errors.New("文件名无效，请移除。")
	}
	if file.Size > composerMaxFileSize {
		return nil, "", composerFileTooLarge
	}
	size := file.Size
	if file.Path != "" {
		info, err := os.Stat(file.Path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, "", errors.New("无法读取此文件，请移除。")
		}
		size = info.Size()
		if size > composerMaxFileSize {
			return nil, "", composerFileTooLarge
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	src, err := file.Open()
	if err != nil {
		return nil, "", errors.New("无法打开文件，请移除。")
	}
	defer src.Close()

	var body bytes.Buffer
	body.Grow(int(max(0, size)) + 1024)
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", file.Name)
	if err != nil {
		return nil, "", errors.New("无法准备附件，请移除。")
	}
	// The selected file can change after the picker returns its size.
	copied, err := io.Copy(part, io.LimitReader(composerFileReader{ctx, src}, composerMaxFileSize+1))
	if err != nil {
		return nil, "", errors.New("无法读取此文件，请移除。")
	}
	if copied > composerMaxFileSize {
		return nil, "", composerFileTooLarge
	}
	if err := writer.Close(); err != nil {
		return nil, "", errors.New("无法准备附件，请移除。")
	}
	return &body, writer.FormDataContentType(), nil
}

type composerFileReader struct {
	ctx context.Context
	src io.Reader
}

func (r composerFileReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.src.Read(p)
}
