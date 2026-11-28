package server

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"slices"

	"github.com/recally-io/dex/internal/api"
)

func (h *handler) ListArtifacts(ctx context.Context, _ api.ListArtifactsRequestObject) (api.ListArtifactsResponseObject, error) {
	rows, err := h.Artifacts.List(botID(ctx))
	if err != nil {
		return nil, err
	}
	out := make([]api.Artifact, 0, len(rows))
	for _, a := range rows {
		out = append(out, api.Artifact{Name: a.Name, Size: a.Size, ModifiedAt: a.ModifiedAt})
	}
	return api.ListArtifacts200JSONResponse{Artifacts: out}, nil
}

func (h *handler) RenameArtifact(ctx context.Context, req api.RenameArtifactRequestObject) (api.RenameArtifactResponseObject, error) {
	a, err := h.Artifacts.Rename(botID(ctx), req.ArtifactName, req.Body.Name)
	if err != nil {
		return nil, err
	}
	return api.RenameArtifact200JSONResponse{Name: a.Name, Size: a.Size, ModifiedAt: a.ModifiedAt}, nil
}

func (h *handler) DeleteArtifact(ctx context.Context, req api.DeleteArtifactRequestObject) (api.DeleteArtifactResponseObject, error) {
	if err := h.Artifacts.Delete(botID(ctx), req.ArtifactName); err != nil {
		return nil, err
	}
	return api.DeleteArtifact200JSONResponse{Status: api.OkStatusOk}, nil
}

func (h *handler) GetArtifact(ctx context.Context, req api.GetArtifactRequestObject) (api.GetArtifactResponseObject, error) {
	a, f, err := h.Artifacts.Open(botID(ctx), req.ArtifactName)
	if os.IsNotExist(err) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, err
	}
	head := make([]byte, 512)
	n, err := f.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		_ = f.Close()
		return nil, err
	}
	contentType, disposition := "application/octet-stream", "attachment"
	if detected := http.DetectContentType(head[:n]); slices.Contains(inlineTypes, detected) {
		contentType, disposition = detected, "inline"
	}
	disposition = mime.FormatMediaType(disposition, map[string]string{"filename": a.Name})
	nosniff := "nosniff"
	return api.GetArtifact200AsteriskResponse{Body: f, ContentType: contentType, ContentLength: a.Size, Headers: api.GetArtifact200ResponseHeaders{ContentDisposition: &disposition, XContentTypeOptions: &nosniff}}, nil
}
