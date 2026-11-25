package app_test

import (
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/plugin"
	"github.com/recally-io/dex/plugins/github"
)

func TestUserKeepsPluginRowsFromBeforePluginMigrations(t *testing.T) {
	t.Parallel()
	// Given a database from before plugin migrations: plugin tables without the prefix, with rows,
	// and GitHub and Webhook on but email off.
	dataDir := t.TempDir()
	entries, err := os.ReadDir("../db/migrations")
	if err != nil {
		t.Fatal(err)
	}
	core := fstest.MapFS{}
	for _, entry := range entries {
		if name := entry.Name(); strings.HasSuffix(name, ".sql") && name < "20261121200000_" {
			content, err := os.ReadFile(filepath.Join("../db/migrations", name))
			if err != nil {
				t.Fatal(err)
			}
			core[name] = &fstest.MapFile{Data: content}
		}
	}
	old, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "control.sqlite")+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, old, core)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("testdata/pre_plugin_migrations.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		string(schema),
		"INSERT INTO plugin_settings (name, enabled) VALUES ('webhook', 1), ('email', 0)",
		"INSERT INTO github_webhook_settings (repository_id, secret, hold_unmatched) VALUES (10, '" + strings.Repeat("g", 40) + "', 1)",
		"INSERT INTO webhook_sources (id, secret) VALUES ('calendar', '" + strings.Repeat("w", 40) + "')",
		"INSERT INTO email_sources (id, secret) VALUES ('mail', '" + strings.Repeat("m", 40) + "')",
		// Port 1 on loopback refuses the connection, so the Pull job reaches no server and keeps the cursor.
		"INSERT INTO email_imap_pulls (source_id, address, username, password, folder, configuration_id, uid_validity, last_uid) VALUES ('mail', 'imap://127.0.0.1:1', 'owner', 'app-password', 'Dex', 'c1', 7, 42)",
	} {
		if _, err := old.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	// When dex starts.
	d := startDex(t, dataDir, "")
	conn := controlWriteDB(t, dataDir)
	count := func(query string) int {
		t.Helper()
		var n int
		if err := conn.QueryRowContext(t.Context(), query).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}
	tables := func() []string {
		t.Helper()
		// Legacy names come from the fixture. Other core tables do not belong to plugins.
		rows, err := conn.QueryContext(t.Context(), `
SELECT name FROM sqlite_master
WHERE type = 'table' AND (
    name GLOB 'plugin_github_*' OR name GLOB 'plugin_webhook_*' OR name GLOB 'plugin_email_*'
    OR name IN ('github_webhook_settings', 'github_review_pulls', 'webhook_sources', 'email_sources', 'email_imap_pulls')
)
ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var names []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			names = append(names, name)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return names
	}

	// Then the enabled plugins own their tables under the prefix, with the old rows.
	t.Logf("artifact: tables=%v", tables())
	if got := tables(); !slices.Equal(got, []string{"email_imap_pulls", "email_sources", "plugin_github_review_pulls", "plugin_github_webhook_settings", "plugin_webhook_sources"}) {
		t.Fatalf("only enabled plugins must migrate: %v", got)
	}
	if count("SELECT count(*) FROM plugin_github_webhook_settings WHERE repository_id = 10 AND hold_unmatched = 1") != 1 || count("SELECT count(*) FROM plugin_webhook_sources WHERE id = 'calendar'") != 1 {
		t.Fatal("the plugin rows must survive the rename")
	}
	if got := count("SELECT count(*) FROM applied_plugin_migrations WHERE (plugin, version) IN (VALUES ('github', 1), ('webhook', 1))"); got != 2 || count("SELECT count(*) FROM applied_plugin_migrations WHERE plugin = 'email'") != 0 {
		t.Fatalf("each enabled plugin must record its versions in the one table: %d", got)
	}
	var plugins api.ListPlugins200JSONResponse
	d.must(t, http.MethodGet, "/api/settings/plugins", nil, &plugins)
	if !slices.Equal(plugins.Plugins, []api.PluginSetting{{Name: "github", Enabled: true, Running: true}, {Name: "webhook", Enabled: true, Running: true}, {Name: "email", Enabled: false, Running: false}}) {
		t.Fatalf("the stored choices must carry over: %+v", plugins.Plugins)
	}

	// When the owner turns email on and dex starts again, email adopts its tables too. The IMAP
	// Pull still follows its source after both renames.
	d.must(t, http.MethodPut, "/api/settings/plugins/email", map[string]bool{"enabled": true}, nil, http.StatusNoContent)
	d.stop()
	d = startDex(t, dataDir, "")
	if count("SELECT count(*) FROM plugin_email_imap_pulls WHERE source_id = 'mail' AND configuration_id = 'c1' AND uid_validity = 7 AND last_uid = 42") != 1 {
		t.Fatalf("the IMAP Pull must survive the rename: %v", tables())
	}
	d.must(t, http.MethodDelete, "/api/plugins/email/sources/mail", nil, nil, http.StatusNoContent)
	if count("SELECT count(*) FROM plugin_email_imap_pulls") != 0 {
		t.Fatal("deleting the source must still delete its Pull")
	}
	d.stop()
	_ = startDex(t, dataDir, "")
	if got := count("SELECT count(*) FROM applied_plugin_migrations WHERE plugin = 'email'"); got != 2 {
		t.Fatalf("a restart must not apply a version again: %d", got)
	}
}

func TestUserKeepsDexRunningWhenAPluginMigrationFails(t *testing.T) {
	t.Parallel()
	// Given an enabled plugin whose migration fails.
	broken := func(plugin.Deps) plugin.Plugin {
		hooks := http.NewServeMux()
		hooks.HandleFunc("POST /hooks/broken/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
		return plugin.Plugin{Name: "broken", Hooks: hooks, HookLimit: 1 << 10, Migrations: fstest.MapFS{
			"migrations/00001_items.sql": {Data: []byte("-- +goose Up\nCREATE TABLE plugin_broken_items (id INTEGER PRIMARY KEY) STRICT;\nINSERT INTO missing_table VALUES (1);\n")},
		}}
	}
	cfg := app.Config{DataDir: t.TempDir(), Plugins: []plugin.Factory{github.New, broken}}
	d := startDexWith(t, cfg)
	d.must(t, http.MethodPut, "/api/settings/plugins/broken", map[string]bool{"enabled": true}, nil, http.StatusNoContent)
	d.stop()

	// When dex starts again.
	d = startDexWith(t, cfg)

	// Then dex and the other plugins run. The broken plugin has no hook, and its error is visible.
	var health api.Health
	d.must(t, http.MethodGet, "/api/health", nil, &health)
	if !slices.Equal(health.Plugins, []string{"github"}) {
		t.Fatalf("only the healthy plugin must run: %+v", health.Plugins)
	}
	var plugins api.ListPlugins200JSONResponse
	d.must(t, http.MethodGet, "/api/settings/plugins", nil, &plugins)
	t.Logf("artifact: plugins=%+v", plugins.Plugins)
	if len(plugins.Plugins) != 2 || plugins.Plugins[1].Running || plugins.Plugins[1].Error == nil || !strings.Contains(*plugins.Plugins[1].Error, "missing_table") {
		t.Fatalf("the settings must show why the plugin does not run: %+v", plugins.Plugins)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, d.srv.URL+"/hooks/broken/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := d.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("a plugin that failed to migrate must not mount its hooks: %d", res.StatusCode)
	}
	// The failed version left no table and no version row.
	conn := controlWriteDB(t, cfg.DataDir)
	var tables, versions int
	if err := conn.QueryRowContext(t.Context(), "SELECT (SELECT count(*) FROM sqlite_master WHERE name = 'plugin_broken_items'), (SELECT count(*) FROM applied_plugin_migrations WHERE plugin = 'broken' AND version = 1)").Scan(&tables, &versions); err != nil || tables != 0 || versions != 0 {
		t.Fatalf("a failed migration must roll back: %d %d %v", tables, versions, err)
	}

	// When the owner turns it off, the error of the last start no longer shows.
	d.must(t, http.MethodPut, "/api/settings/plugins/broken", map[string]bool{"enabled": false}, nil, http.StatusNoContent)
	d.must(t, http.MethodGet, "/api/settings/plugins", nil, &plugins)
	if plugins.Plugins[1].Enabled || plugins.Plugins[1].Error != nil {
		t.Fatalf("a plugin that is off must not show an old error: %+v", plugins.Plugins)
	}
	d.stop()
	d = startDexWith(t, cfg)
	d.must(t, http.MethodGet, "/api/settings/plugins", nil, &plugins)
	if plugins.Plugins[1].Error != nil {
		t.Fatalf("a disabled plugin must not migrate: %+v", plugins.Plugins)
	}
}
