package app_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
)

type agentActivity struct {
	ActiveSessions     int64 `json:"activeSessions"`
	RunningChannels    int64 `json:"runningChannels"`
	EnabledAutomations int64 `json:"enabledAutomations"`
}

func TestUserReadsAllAgentActivityInOneList(t *testing.T) {
	// Given two agents, one running bot, and durable sessions held before worker admission.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	var b apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "B", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, &b, http.StatusCreated)
	d.must(t, http.MethodPost, "/api/maintenance", nil, nil)
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(d.dataDir(t), "control.sqlite")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for i, key := range []string{"main", "chat:busy", "automation:busy", "task:queued", "task:running", "task:done", "task:failed", "task:cancelled", "task:interrupted", "chat:empty", "telegram:old", "dream:hidden"} {
		id := int64(100 + i)
		if _, err := conn.ExecContext(t.Context(), "INSERT INTO conversations (id,agent_id,channel_key,created_at) VALUES (?,'default',?,1)", id, key); err != nil {
			t.Fatal(err)
		}
		kind := "message"
		if key == "chat:empty" {
			kind = "task_result"
		}
		if _, err := conn.ExecContext(t.Context(), "INSERT INTO submissions (id,conversation_id,content,status,created_at,kind) VALUES (?,?,'fixture','pending',1,?)", fmt.Sprint(id), id, kind); err != nil {
			t.Fatal(err)
		}
		if i >= 3 && i <= 8 {
			status := key[len("task:"):]
			if _, err := conn.ExecContext(t.Context(), "INSERT INTO tasks (id,parent_conversation_id,conversation_id,spawn_key,source_request_id,title,status,created_at) VALUES (?,100,?,?,?,'fixture',?,1)", fmt.Sprint(id), id, key, fmt.Sprint(id), status); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A queued task counts even with no pending worker input. Two inputs in a Chat count once.
	for _, stmt := range []string{
		"UPDATE submissions SET status='done' WHERE conversation_id=103",
		"INSERT INTO submissions (id,conversation_id,content,status,created_at) VALUES ('second-input',101,'second','pending',2)",
	} {
		if _, err := conn.ExecContext(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.ExecContext(t.Context(), "INSERT INTO conversations (id,agent_id,channel_key,created_at) VALUES (200,?,'chat:B',1)", b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), "INSERT INTO submissions (id,conversation_id,content,status,created_at) VALUES ('B-input',200,'B','pending',1)"); err != nil {
		t.Fatal(err)
	}
	for i, agent := range []string{"default", "default", "default", b.ID} {
		enabled := 1
		if i == 2 {
			enabled = 0
		}
		if _, err := conn.ExecContext(t.Context(), "INSERT INTO automations (id,agent_id,name,prompt,schedule,time_zone,session,enabled,created_at,updated_at) VALUES (?,?,'fixture','fixture','{}','UTC','reuse',?,1,1)", fmt.Sprint(i), agent, enabled); err != nil {
			t.Fatal(err)
		}
	}
	// When one list request reads all agents, then activity uses session status and runtime state.
	var listed struct {
		Agents []struct {
			ID       string        `json:"id"`
			Activity agentActivity `json:"activity"`
		} `json:"agents"`
	}
	d.must(t, http.MethodGet, "/api/agents", nil, &listed)
	want := map[string]agentActivity{
		"default": {ActiveSessions: 4, RunningChannels: 1, EnabledAutomations: 2},
		b.ID:      {ActiveSessions: 1, RunningChannels: 0, EnabledAutomations: 1},
	}
	if len(listed.Agents) != len(want) {
		t.Fatalf("agents=%+v", listed)
	}
	for _, agent := range listed.Agents {
		if agent.Activity != want[agent.ID] {
			t.Fatalf("agent %s activity=%+v want=%+v", agent.ID, agent.Activity, want[agent.ID])
		}
		var sessions struct {
			Chats []struct {
				Busy bool `json:"busy"`
				Task *struct {
					Status string `json:"status"`
				} `json:"task"`
			} `json:"chats"`
		}
		d.must(t, http.MethodGet, "/api/agents/"+agent.ID+"/chats", nil, &sessions)
		var active int64
		for _, session := range sessions.Chats {
			if session.Task != nil {
				if session.Task.Status == "queued" || session.Task.Status == "running" {
					active++
				}
			} else if session.Busy {
				active++
			}
		}
		if active != agent.Activity.ActiveSessions {
			t.Fatalf("agent %s list count=%d activity=%+v", agent.ID, active, agent.Activity)
		}
	}
	t.Logf("artifact: activity=%+v; session counts match list behavior; multiple inputs count once", listed)
}
