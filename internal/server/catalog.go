package server

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/engine"
)

const (
	skillCatalogOrigin = "https://api.claude-plugins.dev"
	mcpRegistryOrigin  = "https://registry.modelcontextprotocol.io"
	catalogPageSize    = 20
	catalogBodyLimit   = 2 << 20
)

var catalogClient = &http.Client{
	Timeout:       8 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func readCatalog(ctx context.Context, endpoint string, query url.Values, out any) error {
	// Build a new request so that no owner headers or cookies leave Dex.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := catalogClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, catalogBodyLimit+1))
	if err != nil {
		return err
	}
	if len(raw) > catalogBodyLimit {
		return errors.New("catalog page exceeds the response limit")
	}
	return json.Unmarshal(raw, out)
}

func catalogError(ctx context.Context, catalog string, err error) api.Error {
	if ctx.Err() == nil {
		slog.WarnContext(ctx, "catalog search failed", "catalog", catalog, "err", err)
	}
	return api.Error{Error: "The " + catalog + " is unavailable. Try again."}
}

func (h *handler) SearchSkillCatalog(ctx context.Context, req api.SearchSkillCatalogRequestObject) (api.SearchSkillCatalogResponseObject, error) {
	q := strings.TrimSpace(deref(req.Params.Q))
	offset := 0
	if req.Params.Offset != nil {
		offset = *req.Params.Offset
	}
	if utf8.RuneCountInString(q) > 200 || offset < 0 || offset > math.MaxInt-catalogPageSize {
		return nil, errInvalid
	}
	var upstream struct {
		Skills []struct {
			api.SkillCatalogEntry
			Metadata struct {
				DirectoryPath *string `json:"directoryPath"`
			} `json:"metadata"`
		} `json:"skills"`
		Total int `json:"total"`
		Limit int `json:"limit"`
	}
	endpoint := strings.TrimRight(cmp.Or(h.SkillCatalogURL, skillCatalogOrigin), "/") + "/api/skills/search"
	err := readCatalog(ctx, endpoint, url.Values{
		"q": {q}, "offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(catalogPageSize)},
	}, &upstream)
	if err == nil && upstream.Skills == nil {
		err = errors.New("missing skills array")
	}
	if err != nil {
		return api.SearchSkillCatalog502JSONResponse(catalogError(ctx, "skill catalog", err)), nil
	}
	page := api.SkillCatalogPage{Skills: make([]api.SkillCatalogEntry, 0, len(upstream.Skills))}
	for _, skill := range upstream.Skills {
		entry := skill.SkillCatalogEntry
		entry.DirectoryPath = skill.Metadata.DirectoryPath
		page.Skills = append(page.Skills, entry)
	}
	if step := min(upstream.Limit, catalogPageSize); step > 0 && offset+step < upstream.Total && len(page.Skills) > 0 {
		page.NextOffset = new(offset + step)
	}
	return api.SearchSkillCatalog200JSONResponse(page), nil
}

type registryServer struct {
	Name        string  `json:"name"`
	Title       *string `json:"title"`
	Description string  `json:"description"`
	Version     string  `json:"version"`
	Remotes     []struct {
		Type    string                 `json:"type"`
		URL     string                 `json:"url"`
		Headers []api.McpCatalogHeader `json:"headers"`
	} `json:"remotes"`
	Packages []struct{} `json:"packages"`
}

func (s registryServer) catalogEntry() api.McpCatalogEntry {
	entry := api.McpCatalogEntry{
		Name: s.Name, Title: s.Title, Description: s.Description, Version: s.Version,
		Remotes: []api.McpCatalogRemote{},
	}
	for _, remote := range s.Remotes {
		if remote.Type != "streamable-http" {
			continue
		}
		r := api.McpCatalogRemote{Headers: remote.Headers}
		if r.Headers == nil {
			r.Headers = []api.McpCatalogHeader{}
		}
		for i := range r.Headers {
			if r.Headers[i].IsSecret {
				r.Headers[i].Value = nil
			}
		}
		switch {
		case strings.ContainsAny(remote.URL, "{}"):
			r.Reason = new("URL variables must be filled in before adding this remote.")
		case engine.ValidateRemoteMCPURL(remote.URL) != nil:
			r.Reason = new("The remote URL does not meet Dex requirements.")
		default:
			r.Url, r.Addable = remote.URL, true
		}
		entry.Addable = entry.Addable || r.Addable
		entry.Remotes = append(entry.Remotes, r)
	}
	if !entry.Addable {
		switch {
		case len(entry.Remotes) != 0:
			entry.Reason = entry.Remotes[0].Reason
		case len(s.Remotes) == 0 && len(s.Packages) != 0:
			entry.Reason = new("Requires a local package. Dex can only add remote servers here.")
		default:
			entry.Reason = new("No Streamable HTTP remote.")
		}
	}
	return entry
}

func (h *handler) SearchMcpCatalog(ctx context.Context, req api.SearchMcpCatalogRequestObject) (api.SearchMcpCatalogResponseObject, error) {
	q, cursor := strings.TrimSpace(deref(req.Params.Q)), deref(req.Params.Cursor)
	if utf8.RuneCountInString(q) > 200 || len(cursor) > 1024 {
		return nil, errInvalid
	}
	query := url.Values{"search": {q}, "version": {"latest"}, "limit": {strconv.Itoa(catalogPageSize)}}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	var upstream struct {
		Servers []struct {
			Server registryServer `json:"server"`
		} `json:"servers"`
		Metadata struct {
			NextCursor string `json:"nextCursor"`
		} `json:"metadata"`
	}
	endpoint := strings.TrimRight(cmp.Or(h.McpRegistryURL, mcpRegistryOrigin), "/") + "/v0/servers"
	err := readCatalog(ctx, endpoint, query, &upstream)
	if err == nil && upstream.Servers == nil {
		err = errors.New("missing servers array")
	}
	if err != nil {
		return api.SearchMcpCatalog502JSONResponse(catalogError(ctx, "MCP Registry", err)), nil
	}
	page := api.McpCatalogPage{Servers: make([]api.McpCatalogEntry, 0, len(upstream.Servers))}
	for _, item := range upstream.Servers {
		page.Servers = append(page.Servers, item.Server.catalogEntry())
	}
	if next := upstream.Metadata.NextCursor; next != "" && next != cursor {
		page.NextCursor = new(next)
	}
	return api.SearchMcpCatalog200JSONResponse(page), nil
}
