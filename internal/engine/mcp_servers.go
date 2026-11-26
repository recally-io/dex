package engine

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// MCPStore owns control-host MCP declarations and their reload revisions.
type MCPStore struct {
	db         *sql.DB
	mu         sync.Mutex
	revisions  map[string]uint64
	oauthLocks [64]sync.Mutex
}

func NewMCPStore(db *sql.DB) *MCPStore {
	return &MCPStore{db: db, revisions: make(map[string]uint64)}
}

func (s *MCPStore) stamp(botID string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("|mcp:%d:%d|", s.revisions[""], s.revisions[botID])
}

func (s *MCPStore) list(ctx context.Context, botID string) ([]serverEntry, error) {
	if s == nil {
		return nil, nil
	}
	rows, err := sqlc.New(s.db).ListMCPServers(ctx, botID)
	if err != nil {
		return nil, err
	}
	servers := make([]serverEntry, 0, len(rows))
	positions := make(map[string]int, len(rows))
	for _, row := range rows {
		var c serverConfig
		if err := json.Unmarshal([]byte(row.Config), &c); err != nil {
			return nil, &privateError{message: "invalid stored MCP declaration", cause: err}
		}
		pos, override := positions[row.Name]
		info := McpServerInfo{Name: row.Name, Scope: "dex", Overrides: override, State: "idle", Tools: []string{}, VaultReferences: listMCPVaultReferences(c)}
		if row.BotID != "" {
			info.Scope = "agent"
		}
		switch {
		case c.Type == "http":
			info.Transport = "http"
		case c.Command != "":
			info.Transport = "stdio"
		}
		if c.Disabled {
			info.State = "disabled"
		}
		entry := serverEntry{info: info, config: c, scopeID: row.BotID}
		if override {
			servers[pos] = entry
		} else {
			positions[row.Name] = len(servers)
			servers = append(servers, entry)
		}
	}
	return servers, nil
}

func (s *MCPStore) get(ctx context.Context, botID, name string) (serverConfig, error) {
	return getMCPConfig(ctx, sqlc.New(s.db), botID, name)
}

func getMCPConfig(ctx context.Context, q *sqlc.Queries, botID, name string) (serverConfig, error) {
	var c serverConfig
	raw, err := q.GetMCPServer(ctx, sqlc.GetMCPServerParams{BotID: botID, Name: name})
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return c, &privateError{message: "invalid stored MCP declaration", cause: err}
	}
	return c, nil
}

func (s *MCPStore) set(ctx context.Context, botID, name string, c serverConfig) error {
	if !serverName.MatchString(name) || (!c.Disabled && ((c.Type == "http" && c.URL == "") || (c.Type != "http" && (c.Command == "" || c.Type != "" && c.Type != "stdio")))) {
		return fmt.Errorf("%w: invalid MCP server declaration", ErrInvalidInput)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := sqlc.New(tx)
	previous, err := getMCPConfig(ctx, q, botID, name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := keepMCPValues(c.Env, previous.Env); err != nil {
		return err
	}
	if err := keepMCPValues(c.Headers, previous.Headers); err != nil {
		return err
	}
	if strings.HasSuffix(c.URL, "?"+hiddenMCPValue) {
		base, query, found := strings.Cut(previous.URL, "?")
		if !found || c.URL != base+"?"+hiddenMCPValue {
			return fmt.Errorf("%w: no stored query for this URL", ErrInvalidInput)
		}
		c.URL = base + "?" + query
	}
	if err := validateMCPOAuth(c); err != nil {
		return err
	}
	c.OAuthID = ""
	if c.OAuth != nil {
		if previous.OAuth != nil && *previous.OAuth == *c.OAuth && previous.URL == c.URL && previous.Disabled == c.Disabled {
			c.OAuthID = previous.OAuthID
		}
		if c.OAuthID == "" {
			c.OAuthID = uuid.NewV4().String()
		}
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return &privateError{message: "cannot encode MCP declaration", cause: err}
	}
	if err := q.SetMCPServer(ctx, sqlc.SetMCPServerParams{BotID: botID, Name: name, Config: string(raw)}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.revisions[botID]++
	return nil
}

func (s *MCPStore) remove(ctx context.Context, botID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// One SQL statement is a transaction. The revision changes only after it commits.
	count, err := sqlc.New(s.db).RemoveMCPServer(ctx, sqlc.RemoveMCPServerParams{BotID: botID, Name: name})
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	s.revisions[botID]++
	return nil
}

const hiddenMCPValue = "[hidden]"

var errMCPIncompleteOAuth = errors.New("this MCP declaration has an OAuth identity but no OAuth settings, so dex does not connect it; ask the owner for the intended settings and replace the declaration with mcp_server_set")

// incompleteOAuth reports a stored declaration whose OAuth settings are
// missing next to its OAuth identity, such as a row from an older writer.
// Dex must not connect it as plain HTTP or infer OAuth settings or a grant.
func (c serverConfig) incompleteOAuth() bool { return c.OAuth == nil && c.OAuthID != "" }

var mcpOAuthSecret = regexp.MustCompile(`^vault:[A-Za-z_][A-Za-z0-9_]*$`)

var mcpVaultReference = regexp.MustCompile(`^vault:[A-Za-z_][A-Za-z0-9_]*$|\$\{vault:[A-Za-z_][A-Za-z0-9_]*\}`)

type mcpVaultReferences struct {
	Env     map[string]string `json:"env,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

func listMCPVaultReferences(c serverConfig) *mcpVaultReferences {
	references := func(values map[string]string) map[string]string {
		out := map[string]string{}
		for key, value := range values {
			if mcpVaultReference.MatchString(value) {
				out[key] = value
			}
		}
		return out
	}
	env, headers := references(c.Env), references(c.Headers)
	if len(env)+len(headers) == 0 {
		return nil
	}
	return &mcpVaultReferences{Env: env, Headers: headers}
}

func redactMCPConfig(c serverConfig) serverConfig {
	c.OAuthID = ""
	if c.OAuth != nil && c.OAuth.ClientSecret != "" && !mcpOAuthSecret.MatchString(c.OAuth.ClientSecret) {
		redacted := *c.OAuth
		redacted.ClientSecret = hiddenMCPValue
		c.OAuth = &redacted
	}
	if c.Args == nil {
		c.Args = []string{}
	}
	c.Env = redactMCPValues(c.Env)
	c.Headers = redactMCPValues(c.Headers)
	if base, _, ok := strings.Cut(c.URL, "?"); ok {
		c.URL = base + "?" + hiddenMCPValue
	}
	return c
}

func redactMCPValues(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = hiddenMCPValue
		if mcpVaultReference.MatchString(value) {
			out[key] = value
		}
	}
	return out
}

func keepMCPValues(values, stored map[string]string) error {
	for key, value := range values {
		if value == hiddenMCPValue {
			previous, ok := stored[key]
			if !ok {
				return fmt.Errorf("%w: no stored value to keep", ErrInvalidInput)
			}
			values[key] = previous
		}
	}
	return nil
}
