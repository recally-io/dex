package db_test

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/settings"
)

func TestUserUpgradesMainChannelsAndPairsTelegram(t *testing.T) {
	for _, version := range []string{"20261015113034", "20261015120000"} {
		t.Run(version, func(t *testing.T) {
			// Given a main database before Telegram pairing, with saved channel data.
			entries, err := os.ReadDir("migrations")
			if err != nil {
				t.Fatal(err)
			}
			mainSchema := fstest.MapFS{}
			for _, entry := range entries {
				name := entry.Name()
				if strings.HasSuffix(name, "_telegram_pairing.sql") || strings.SplitN(name, "_", 2)[0] > version {
					continue
				}
				data, err := os.ReadFile(filepath.Join("migrations", name))
				if err != nil {
					t.Fatal(err)
				}
				mainSchema[name] = &fstest.MapFile{Data: data}
			}
			path := filepath.Join(t.TempDir(), "control.sqlite")
			old, err := sql.Open("sqlite", "file:"+path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = old.Close() })
			provider, err := goose.NewProvider(goose.DialectSQLite3, old, mainSchema)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provider.Up(t.Context()); err != nil {
				t.Fatal(err)
			}
			statements := []string{
				`INSERT INTO agents (id,name,model_provider,model_id,created_at) VALUES ('default','Dex','faux','echo',1)`,
				`INSERT INTO telegram_settings (id,token,allowed_users) VALUES (1,'7:upgrade','')`,
				`INSERT INTO wechat_settings VALUES (1,'wx-token','https://example.com','wx-bot','wx-owner')`,
				`INSERT INTO wechat_runtime VALUES ('wx-bot','wx-owner','cursor','context','18446744073709551615',123)`,
				`INSERT INTO channel_peers VALUES ('wechat:wx-owner','peer/saved')`,
				`INSERT INTO conversations (id,agent_id,channel_key,created_at) VALUES (1,'default','peer/saved',1)`,
				`INSERT INTO submissions (id,conversation_id,content,status,answer,created_at,settled_at,delivered_parts) VALUES ('saved',1,'hi','done','answer',1,2,1)`,
				`INSERT INTO channel_commands (source_key,peer,channel_key,command,answer,completed) VALUES ('command','wechat:wx-owner','peer/saved','/help','help',1)`,
			}
			if version == "20261015120000" {
				statements = append(statements, `INSERT INTO feishu_settings VALUES (1,'fs-app','fs-secret','lark','fs-owner','fs-code',123)`)
			}
			for _, stmt := range statements {
				if _, err := old.ExecContext(t.Context(), stmt); err != nil {
					t.Fatal(err)
				}
			}
			if err := old.Close(); err != nil {
				t.Fatal(err)
			}

			// When dex upgrades the database with the normal Goose policy.
			conn, err := db.Open(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })

			// Then settings, runtime state, chats, and delivery progress stay unchanged.
			checks := map[string]string{
				`SELECT token || '|' || allowed_users FROM telegram_settings`:                                                                         "7:upgrade|",
				`SELECT token || '|' || base_url || '|' || bot_id || '|' || owner FROM wechat_settings`:                                               "wx-token|https://example.com|wx-bot|wx-owner",
				`SELECT cursor || '|' || context_token || '|' || context_message_id || '|' || pause_until FROM wechat_runtime`:                        "cursor|context|18446744073709551615|123",
				`SELECT peer || '|' || channel_key FROM channel_peers`:                                                                                "wechat:wx-owner|peer/saved",
				`SELECT agent_id || '|' || channel_key FROM conversations`:                                                                            "default|peer/saved",
				`SELECT content || '|' || status || '|' || answer || '|' || delivered_parts || '|' || COALESCE(delivered_at,'null') FROM submissions`: "hi|done|answer|1|null",
				`SELECT source_key || '|' || peer || '|' || channel_key || '|' || command || '|' || answer || '|' || completed FROM channel_commands`: "command|wechat:wx-owner|peer/saved|/help|help|1",
			}
			if version == "20261015120000" {
				checks[`SELECT app_id || '|' || app_secret || '|' || brand || '|' || owner_id || '|' || pairing_code || '|' || pairing_expires_at FROM feishu_settings`] = "fs-app|fs-secret|lark|fs-owner|fs-code|123"
			}
			for query, want := range checks {
				var got string
				if err := conn.QueryRowContext(t.Context(), query).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Fatalf("%s: got %q, want %q", query, got, want)
				}
			}

			// And the upgraded Telegram bot can create and consume an owner link once.
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/bot7:upgrade/getMe" {
					t.Errorf("unexpected Telegram request: %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write([]byte(`{"ok":true,"result":{"id":7,"is_bot":true,"username":"upgrade_bot"}}`))
			}))
			t.Cleanup(api.Close)
			prefs := settings.New(sqlc.New(conn), nil, api.URL, nil)
			link, _, err := prefs.CreateTelegramPairing(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := url.Parse(link)
			if err != nil {
				t.Fatal(err)
			}
			config, err := prefs.TelegramConfig(t.Context())
			if err != nil || config == nil {
				t.Fatalf("Telegram config: %v, %v", config, err)
			}
			code := parsed.Query().Get("start")
			users, err := config.Pair(t.Context(), code, 42)
			if err != nil || !slices.Equal(users, []int64{42}) {
				t.Fatalf("paired users: %v, %v", users, err)
			}
			users, err = config.Pair(t.Context(), code, 99)
			if err != nil || len(users) != 0 {
				t.Fatalf("reused link: %v, %v", users, err)
			}
			config, err = prefs.TelegramConfig(t.Context())
			if err != nil || config == nil || !slices.Equal(config.AllowedUsers, []int64{42}) {
				t.Fatalf("saved owner: %v, %v", config, err)
			}
		})
	}
}
