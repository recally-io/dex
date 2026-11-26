package app_test

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/sysops"
)

func TestUserManagesMCPServersFromRunner(t *testing.T) {
	t.Parallel()
	// Given an owner session on a real runner connection.
	d := startDexHome(t, app.Config{DataDir: t.TempDir()}, t.TempDir())
	m := enrollMachine(t, d)
	m.connect(d)
	d.setModel(t, "tool")
	var opened sessionInfo
	d.must(t, http.MethodPost, "/api/bots/default/sessions", nil, &opened, http.StatusCreated)
	d.must(t, http.MethodPut, fmt.Sprintf("/api/bots/default/sessions/%d/runner", opened.ID), map[string]string{"runnerId": m.envID}, nil)
	session := botSession{bots: &bots{t: t, d: d}, bot: "default", id: opened.ID}
	server := httpServer(t, t.TempDir())
	url := server["url"].(string)

	// When the bot adds an HTTP server, wait for the automatic reload without a new turn.
	session.send(`mcp_server_set {"name":"files","level":"dex","config":{"type":"http","url":"` + url + `"}}`)
	session.watch().until(t, "write result", answeredWith("tool: "))
	settled := time.Now()
	var apiServers []serverInfo
	eventually(t, "new MCP connection", func() (bool, any) {
		info := d.extensions(t)
		apiServers = info.Servers
		return info.server("files").State == "connected" && slices.Contains(info.server("files").Tools, "append"), info.Servers
	})

	if elapsed := time.Since(settled); elapsed >= 2800*time.Millisecond {
		t.Fatalf("new MCP tools became available too late after settlement: %s", elapsed)
	}

	// Then the list tool and the Extensions API (also used by the web) agree, and the runner session can call the new tool.
	session.send(`mcp_server_list {}`)
	w := session.watch()
	w.until(t, "list matches API", func(tr *transcript) bool {
		for _, item := range tr.items {
			if raw, found := strings.CutPrefix(item.Text, "tool: ["); found {
				var listed []serverInfo
				return json.Unmarshal([]byte("["+raw), &listed) == nil && reflect.DeepEqual(listed, apiServers)
			}
		}
		return false
	})
	session.send(`mcp__files__append {"text":"new"}`)
	w.until(t, "MCP answer", answered("tool: appended 1"))

	// A bot-level override disables the server for this bot only.
	session.send(`mcp_server_set {"name":"files","level":"agent","config":{"disabled":true}}`)
	eventually(t, "disabled override", func() (bool, any) {
		got := d.extensions(t).server("files")
		return got.Scope == "agent" && got.Overrides && got.State == "disabled", got
	})
	var second apiBot
	d.must(t, http.MethodPost, "/api/bots", map[string]string{"name": "Other bot", "provider": "faux", "modelId": "tool"}, &second, http.StatusCreated)
	var other extensions
	d.must(t, http.MethodGet, "/api/bots/"+second.ID+"/integrations", nil, &other)
	if got := other.server("files"); got.Scope != "dex" || got.Overrides || got.State != "idle" {
		t.Fatalf("override affected the other bot: %+v", got)
	}
}

func TestUserEditsMCPWithoutReadingSecretsAndBacksUpDeclarations(t *testing.T) {
	t.Parallel()
	// Given stored declarations, with a plain secret and a vault reference on the first server.
	data := t.TempDir()
	const secret = "example-no-transcript-credential"
	const args = `{"name":"first","level":"agent"}`
	seedMCP(t, data, "default", map[string]any{
		"first":  map[string]any{"disabled": true, "env": map[string]string{"API_KEY": secret}, "headers": map[string]string{"Authorization": "${vault:NOTION_TOKEN}"}, "url": "https://example.invalid/mcp?token=" + secret},
		"second": map[string]any{"disabled": true},
		"third":  map[string]any{"disabled": true},
	})
	d := startDexHome(t, app.Config{DataDir: data, Version: "v1.0.0"}, t.TempDir())
	d.setModel(t, "tool")
	w := d.watch(t, d.newSessionWithMessage(t, "mcp_server_get "+args))
	w.until(t, "redacted declaration", func(tr *transcript) bool {
		for _, item := range tr.items {
			if strings.Contains(item.Text, secret) {
				t.Fatal("tool result contains the plain secret")
			}
			if strings.HasPrefix(item.Text, "tool: ") && strings.Contains(item.Text, `"API_KEY":"[hidden]"`) && strings.Contains(item.Text, `${vault:NOTION_TOKEN}`) && strings.Contains(item.Text, `?[hidden]`) {
				return true
			}
		}
		return false
	})

	// When the bot edits the args, tries an invalid replacement, and deletes a different declaration.
	call := func(input, answer string) {
		t.Helper()
		d.watch(t, d.newSessionWithMessage(t, input)).until(t, input, answeredWith(answer))
	}
	call(`mcp_server_set {"name":"first","level":"agent","config":{"disabled":true,"args":["new"],"env":{"API_KEY":"[hidden]"},"headers":{"Authorization":"${vault:NOTION_TOKEN}"},"url":"https://example.invalid/mcp?[hidden]"}}`, "tool: ")
	call(`mcp_server_set {"name":"first","level":"agent","config":{"type":"http"}}`, "tool error: ")
	call(`mcp_server_remove {"name":"second","level":"agent"}`, "tool: ")

	// Then the live backup shows the updated args, unchanged secret, and remaining order.
	var backup api.SystemBackup
	d.must(t, http.MethodPost, "/api/system/backup", nil, &backup)
	conn, err := db.Open(t.Context(), filepath.Join(sysops.BackupsDir(data), backup.Name))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	rows, err := sqlc.New(conn).ListMCPServers(t.Context(), "default")
	if err != nil || len(rows) != 2 || rows[0].Name != "first" || rows[1].Name != "third" {
		t.Fatalf("backup MCP declaration order: got %d rows, err %v", len(rows), err)
	}
	var first struct {
		Args []string          `json:"args"`
		Env  map[string]string `json:"env"`
		URL  string            `json:"url"`
	}
	if err := json.Unmarshal([]byte(rows[0].Config), &first); err != nil || !slices.Equal(first.Args, []string{"new"}) || first.Env["API_KEY"] != secret || !strings.HasSuffix(first.URL, "token="+secret) {
		t.Fatal("backup lost the last valid edit or its retained secret")
	}
}
