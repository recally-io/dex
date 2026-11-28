package server

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/bot"
	"github.com/recally-io/dex/internal/upload"
)

func (h *handler) uploads(ctx context.Context) *upload.Store {
	return upload.New(bot.Home(h.DataDir, botID(ctx)))
}

// CreateUpload stores the first part named file. The browser sends the file's base name.
func (h *handler) CreateUpload(ctx context.Context, req api.CreateUploadRequestObject) (api.CreateUploadResponseObject, error) {
	for {
		part, err := req.Body.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, errors.Join(errInvalid, errors.New("the request has no file"))
		}
		if err != nil {
			return nil, uploadError(err)
		}
		if part.FormName() != "file" {
			continue
		}
		// Part.FileName drops the directories of the name; the raw parameter shows them, so a name
		// with a path is refused, not changed.
		_, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if err != nil {
			return nil, errors.Join(errInvalid, err)
		}
		a, err := h.uploads(ctx).Save(params["filename"], part)
		if err != nil {
			return nil, uploadError(err)
		}
		return api.CreateUpload201JSONResponse(a), nil
	}
}

// uploadError reports a body that is larger than the server reads as a file that is too large.
func uploadError(err error) error {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return upload.ErrTooLarge
	}
	return err
}

// Images that browsers show inline. Other types, SVG too, can run scripts in a browser, so they come
// only as downloads.
var inlineTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}

func (h *handler) GetUpload(ctx context.Context, req api.GetUploadRequestObject) (api.GetUploadResponseObject, error) {
	f, a, err := h.uploads(ctx).Open(req.UploadId)
	if err != nil {
		return nil, err
	}
	contentType, disposition := "application/octet-stream", "attachment"
	if slices.Contains(inlineTypes, a.MimeType) {
		contentType, disposition = a.MimeType, "inline"
	}
	disposition = mime.FormatMediaType(disposition, map[string]string{"filename": a.Name})
	nosniff := "nosniff"
	return api.GetUpload200AsteriskResponse{
		Body: f, ContentType: contentType, ContentLength: a.Size,
		Headers: api.GetUpload200ResponseHeaders{ContentDisposition: &disposition, XContentTypeOptions: &nosniff},
	}, nil
}
