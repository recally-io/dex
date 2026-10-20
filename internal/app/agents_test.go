package app_test

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"slices"
	"strings"
	"testing"
	"uuid"
)

type apiAgent struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	ModelID       string `json:"modelId"`
	ThinkingLevel string `json:"thinkingLevel"`
	Instructions  string `json:"instructions"`
}

func TestUserCreatesAndValidatesAgents(t *testing.T) {
	// Given dex with its default agent.
	d := startDex(t, t.TempDir(), "")
	var listed struct {
		Agents []apiAgent `json:"agents"`
	}
	d.must(t, http.MethodGet, "/api/agents", nil, &listed)
	if len(listed.Agents) != 1 || listed.Agents[0].ID != "default" {
		t.Fatalf("initial agents=%+v", listed)
	}
	// When the owner creates a second agent without instructions.
	input := map[string]string{"name": " Second ", "provider": "faux", "modelId": "echo", "thinkingLevel": "off", "id": "default"}
	var created apiAgent
	d.must(t, http.MethodPost, "/api/agents", input, &created, http.StatusCreated)
	id, err := uuid.Parse(created.ID)
	if err != nil || id[6]>>4 != 7 || created.Name != "Second" || created.Instructions != "" {
		t.Fatalf("created=%+v uuid error=%v", created, err)
	}
	base := "/api/agents/" + created.ID
	var saved apiAgent
	d.must(t, http.MethodGet, base, nil, &saved)
	if saved != created {
		t.Fatalf("saved=%+v created=%+v", saved, created)
	}
	// Then invalid names, models and thinking levels fail at both write paths.
	for _, invalid := range []map[string]string{
		{"name": ""}, {"name": strings.Repeat("字", 41)}, {"provider": "missing"}, {"modelId": "missing"}, {"thinkingLevel": "unknown"},
	} {
		bad := map[string]string{"name": "Second", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}
		maps.Copy(bad, invalid)
		d.must(t, http.MethodPost, "/api/agents", bad, nil, http.StatusBadRequest)
		d.must(t, http.MethodPut, base, bad, nil, http.StatusBadRequest)
	}
	input["name"], input["modelId"], input["instructions"] = "Renamed", "shout", "Second instructions"
	d.must(t, http.MethodPut, base, input, &saved)
	if saved.ID != created.ID || saved.Name != "Renamed" || saved.ModelID != "shout" || saved.Instructions != "Second instructions" {
		t.Fatalf("updated=%+v", saved)
	}
	d.must(t, http.MethodGet, "/api/agents", nil, &listed)
	if len(listed.Agents) != 2 || listed.Agents[1] != saved {
		t.Fatalf("listed=%+v", listed)
	}
	var defaultAgent apiAgent
	d.must(t, http.MethodGet, "/api/agents/default", nil, &defaultAgent)
	if defaultAgent.ID != "default" || defaultAgent.Name != "Dex" {
		t.Fatalf("default changed: %+v", defaultAgent)
	}
	t.Logf("artifact: created agent=%s; list=%+v", created.ID, listed.Agents)
}

func TestUserKeepsAgentChatsAndUploadsSeparate(t *testing.T) {
	// Given two agents whose first execution conversations can have the same ID.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	var b apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "B", "provider": "faux", "modelId": "shout", "thinkingLevel": "off"}, &b, http.StatusCreated)
	aBase, bBase := "/api/agents/default", "/api/agents/"+b.ID
	var aMain, bMain chatInfo
	d.must(t, http.MethodGet, aBase+"/chats/main", nil, &aMain)
	d.must(t, http.MethodGet, bBase+"/chats/main", nil, &bMain)
	if aMain.ID == bMain.ID || aMain.ID != d.mainChat(t) {
		t.Fatalf("main A=%+v B=%+v", aMain, bMain)
	}
	aPath, bPath := fmt.Sprintf("%s/chats/%d", aBase, aMain.ID), fmt.Sprintf("%s/chats/%d", bBase, bMain.ID)
	aWatch, bWatch := d.watchPath(t, aPath+"/events"), d.watchPath(t, bPath+"/events")
	// When each agent receives its own message.
	d.must(t, http.MethodPost, aPath+"/messages", map[string]string{"id": uuid.NewV7().String(), "text": "A secret"}, nil, http.StatusAccepted)
	d.must(t, http.MethodPost, bPath+"/messages", map[string]string{"id": uuid.NewV7().String(), "text": "B secret"}, nil, http.StatusAccepted)
	aWatch.until(t, "A answer", answered("echo: A secret"))
	bWatch.until(t, "B answer", answered("shout: B secret"))
	conn := taskResultDB(t, d.dataDir(t))
	var aExecution, bExecution int64
	for _, c := range []struct {
		id        int64
		execution *int64
	}{{aMain.ID, &aExecution}, {bMain.ID, &bExecution}} {
		if err := conn.QueryRowContext(t.Context(), "SELECT execution_id FROM conversations WHERE id = ?", c.id).Scan(c.execution); err != nil {
			t.Fatal(err)
		}
	}
	if aExecution == 0 || aExecution != bExecution {
		t.Fatalf("expected equal execution conversation IDs: A=%d B=%d", aExecution, bExecution)
	}
	t.Logf("artifact: execution conversation ID=%d in both agents", aExecution)
	// Then snapshots and history use each stream's agent.
	for _, tc := range []struct{ path, want, other string }{{aPath, "A secret", "B secret"}, {bPath, "B secret", "A secret"}} {
		snapshot := d.watchPath(t, tc.path+"/events")
		snapshot.until(t, "snapshot", func(tr *transcript) bool { return tr.snapshots > 0 })
		raw, err := json.Marshal(snapshot.tr.items)
		if err != nil || !bytes.Contains(raw, []byte(tc.want)) || bytes.Contains(raw, []byte(tc.other)) {
			t.Fatalf("snapshot=%s err=%v", raw, err)
		}
		snapshot.cancel()
		status, history := d.call(t, http.MethodGet, tc.path+"/history?before=9007199254740991", nil)
		if status != http.StatusOK || !strings.Contains(history, tc.want) || strings.Contains(history, tc.other) {
			t.Fatalf("history %s: %d %s", tc.path, status, history)
		}
	}
	// And an upload above the small JSON body limit stays inside B's store.
	data := bytes.Repeat([]byte("agent B file\n"), 30000)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "large.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, d.srv.URL+bBase+"/uploads", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := d.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("large upload: %d %s err=%v", resp.StatusCode, raw, err)
	}
	var uploaded attachment
	if err := json.Unmarshal(raw, &uploaded); err != nil {
		t.Fatal(err)
	}
	if uploaded.Size != int64(len(data)) {
		t.Fatalf("upload=%+v", uploaded)
	}
	status, downloaded := d.call(t, http.MethodGet, bBase+"/uploads/"+uploaded.ID, nil)
	if status != http.StatusOK || downloaded != string(data) {
		t.Fatalf("download: status=%d bytes=%d", status, len(downloaded))
	}
	d.must(t, http.MethodGet, aBase+"/uploads/"+uploaded.ID, nil, nil, http.StatusNotFound)
	d.must(t, http.MethodPost, bPath+"/messages", map[string]any{"id": uuid.NewV7().String(), "text": "B attachment", "attachments": []string{uploaded.ID}}, nil, http.StatusAccepted)
	bWatch.until(t, "B attachment answer", answered("shout: B attachment\n\nAttached files:\n- "+uploaded.Path))
	if !slices.ContainsFunc(bWatch.tr.items, func(i item) bool { return len(i.Attachments) == 1 && i.Attachments[0].ID == uploaded.ID }) {
		t.Fatalf("missing attachment: %+v", bWatch.tr.items)
	}
	// And all cross-agent chat entry points refuse A's conversation under B.
	cross := fmt.Sprintf("%s/chats/%d", bBase, aMain.ID)
	for _, tc := range []struct {
		method, suffix string
		body           any
	}{
		{http.MethodGet, "", nil}, {http.MethodGet, "/events", nil}, {http.MethodGet, "/history?before=1", nil},
		{http.MethodPost, "/messages", map[string]string{"id": uuid.NewV7().String(), "text": "cross"}},
		{http.MethodPost, "/abort", nil}, {http.MethodPost, "/clear", map[string]string{"id": uuid.NewV7().String()}},
		{http.MethodPut, "/settings", map[string]string{}},
	} {
		d.must(t, tc.method, cross+tc.suffix, tc.body, nil, http.StatusNotFound)
	}
	var session chatInfo
	d.must(t, http.MethodPost, bBase+"/chats", nil, &session, http.StatusCreated)
	sessionPath := fmt.Sprintf("%s/chats/%d", bBase, session.ID)
	d.must(t, http.MethodPost, sessionPath+"/messages", map[string]string{"id": uuid.NewV7().String(), "text": "B session"}, nil, http.StatusAccepted)
	d.watchPath(t, sessionPath+"/events").until(t, "B session answer", answered("shout: B session"))
	status, list := d.call(t, http.MethodGet, bBase+"/chats", nil)
	if status != http.StatusOK || !strings.Contains(list, "B session") {
		t.Fatalf("B list: %d %s", status, list)
	}
	status, list = d.call(t, http.MethodGet, "/api/agents/default/chats", nil)
	if status != http.StatusOK || strings.Contains(list, "B session") {
		t.Fatalf("A list: %d %s", status, list)
	}
	legacy := d.mustUpload(t, "default.txt", []byte("default"))
	d.must(t, http.MethodGet, aBase+"/uploads/"+legacy.ID, nil, nil)
	d.must(t, http.MethodGet, bBase+"/uploads/"+legacy.ID, nil, nil, http.StatusNotFound)
	t.Logf("artifact: main A=%d B=%d; B upload=%s size=%d", aMain.ID, bMain.ID, uploaded.ID, uploaded.Size)
}

func TestUserKeepsAgentAutomationsAndTasksSeparate(t *testing.T) {
	// Given a second agent that can spawn native tasks.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	var b apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "B", "provider": "faux", "modelId": "spawn", "thinkingLevel": "off"}, &b, http.StatusCreated)
	base := "/api/agents/" + b.ID
	input := map[string]any{"name": "B timer", "prompt": "B scheduled prompt", "schedule": map[string]any{"kind": "every", "minutes": 60}, "timeZone": "UTC", "session": "reuse", "enabled": true}
	var auto automationInfo
	d.must(t, http.MethodPost, base+"/automations", input, &auto, http.StatusCreated)
	var listed struct {
		Automations []automationInfo `json:"automations"`
	}
	d.must(t, http.MethodGet, base+"/automations", nil, &listed)
	if len(listed.Automations) != 1 || listed.Automations[0].ID != auto.ID {
		t.Fatalf("B automations=%+v", listed)
	}
	d.must(t, http.MethodGet, "/api/agents/default/automations", nil, &listed)
	if len(listed.Automations) != 0 {
		t.Fatalf("default automations=%+v", listed)
	}
	path := base + "/automations/" + auto.ID
	d.must(t, http.MethodGet, path, nil, nil)
	d.must(t, http.MethodPut, path, input, nil)
	for _, tc := range []struct {
		method, suffix string
		body           any
	}{
		{http.MethodGet, "", nil}, {http.MethodPut, "", input}, {http.MethodDelete, "", nil}, {http.MethodPost, "/run", nil}, {http.MethodGet, "/runs", nil},
	} {
		d.must(t, tc.method, "/api/agents/default/automations/"+auto.ID+tc.suffix, tc.body, nil, http.StatusNotFound)
	}
	// When the owner sends a task request through B's main conversation.
	var main chatInfo
	d.must(t, http.MethodGet, base+"/chats/main", nil, &main)
	text := taskJSON(t, map[string]string{"title": "B task", "goal": "B report", "provider": "faux", "modelId": "echo"})
	d.must(t, http.MethodPost, fmt.Sprintf("%s/chats/%d/messages", base, main.ID), map[string]string{"id": uuid.NewV7().String(), "text": text}, nil, http.StatusAccepted)
	task := waitExecution(t, taskResultDB(t, dir), main.ID, "B task", "done")
	// Then only B can read the saved task and its work.
	var detail taskInfo
	d.must(t, http.MethodGet, base+"/tasks/"+task.ID, nil, &detail)
	if detail.Report != "echo: B report" {
		t.Fatalf("task=%+v", detail)
	}
	d.must(t, http.MethodGet, "/api/agents/default/tasks/"+task.ID, nil, nil, http.StatusNotFound)
	// And B can run and remove its automation without changing the default list.
	d.must(t, http.MethodPut, base, map[string]string{"name": "B", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, nil)
	input["prompt"] = "no delegated work"
	d.must(t, http.MethodPut, path, input, nil)
	var run runInfo
	d.must(t, http.MethodPost, path+"/run", nil, &run, http.StatusAccepted)
	if run.ChatID == 0 {
		t.Fatalf("run=%+v", run)
	}
	d.must(t, http.MethodGet, path+"/runs", nil, nil)
	runPath := fmt.Sprintf("%s/chats/%d", base, run.ChatID)
	d.watchPath(t, runPath+"/events").until(t, "B automation answer", answered("echo: no delegated work"))
	d.must(t, http.MethodPut, runPath+"/settings", map[string]string{"provider": "faux", "modelId": "shout", "thinkingLevel": "high"}, nil)
	d.must(t, http.MethodGet, base+"/extensions", nil, nil)
	d.must(t, http.MethodDelete, path, nil, nil)
	d.must(t, http.MethodGet, path, nil, nil, http.StatusNotFound)
	t.Logf("artifact: B=%s task=%s automation=%s run=%s", b.ID, task.ID, auto.ID, run.ID)
}

func TestUserGetsNotFoundForUnknownAgents(t *testing.T) {
	// Given an ID that is absent from control storage.
	d := startDex(t, t.TempDir(), "")
	base := "/api/agents/" + uuid.NewV7().String()
	// Then every agent-scoped API refuses the unknown ID before doing work.
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "", nil}, {http.MethodPut, "", map[string]string{"name": "Unknown", "thinkingLevel": "off"}},
		{http.MethodGet, "/chats/main", nil}, {http.MethodGet, "/chats", nil}, {http.MethodPost, "/chats", nil},
		{http.MethodGet, "/chats/1", nil}, {http.MethodGet, "/chats/1/events", nil},
		{http.MethodGet, "/chats/1/history?before=1", nil}, {http.MethodPut, "/chats/1/settings", map[string]string{}},
		{http.MethodPost, "/chats/1/messages", map[string]string{"id": uuid.NewV7().String(), "text": "unknown"}},
		{http.MethodPost, "/chats/1/clear", map[string]string{"id": uuid.NewV7().String()}}, {http.MethodPost, "/chats/1/abort", nil},
		{http.MethodGet, "/tasks/missing", nil}, {http.MethodGet, "/uploads/" + uuid.NewV7().String(), nil},
		{http.MethodPost, "/uploads", nil}, {http.MethodGet, "/extensions", nil}, {http.MethodPost, "/extensions/reload", nil},
		{http.MethodGet, "/automations", nil}, {http.MethodPost, "/automations", map[string]string{}},
		{http.MethodGet, "/automations/missing", nil}, {http.MethodPut, "/automations/missing", map[string]string{}},
		{http.MethodDelete, "/automations/missing", nil}, {http.MethodPost, "/automations/missing/run", nil}, {http.MethodGet, "/automations/missing/runs", nil},
	} {
		d.must(t, tc.method, base+tc.path, tc.body, nil, http.StatusNotFound)
	}
}

func TestUserDefaultAgentRequiresExplicitAPIPath(t *testing.T) {
	// Given a new installation with the default agent.
	d := startDex(t, t.TempDir(), "")
	// When a caller uses a removed agent route, then it gets 404.
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/chats/main"},
		{http.MethodGet, "/api/chats"},
		{http.MethodPost, "/api/chats"},
		{http.MethodGet, "/api/chats/1"},
		{http.MethodPut, "/api/chats/1/settings"},
		{http.MethodPost, "/api/chats/1/messages"},
		{http.MethodPost, "/api/chats/1/clear"},
		{http.MethodGet, "/api/chats/1/history"},
		{http.MethodPost, "/api/chats/1/abort"},
		{http.MethodGet, "/api/chats/1/events"},
		{http.MethodGet, "/api/tasks/old"},
		{http.MethodPost, "/api/uploads"},
		{http.MethodGet, "/api/uploads/old"},
		{http.MethodGet, "/api/extensions"},
		{http.MethodPost, "/api/extensions/reload"},
		{http.MethodGet, "/api/automations"},
		{http.MethodPost, "/api/automations"},
		{http.MethodGet, "/api/automations/old"},
		{http.MethodPut, "/api/automations/old"},
		{http.MethodDelete, "/api/automations/old"},
		{http.MethodPost, "/api/automations/old/run"},
		{http.MethodGet, "/api/automations/old/runs"},
		{http.MethodPut, "/api/settings/agent"},
	} {
		status, body := d.call(t, route.method, route.path, nil)
		if status != http.StatusNotFound {
			t.Fatalf("%s %s: status=%d body=%s", route.method, route.path, status, body)
		}
	}
	// Global settings have no implicit agent. The explicit default agent still exists.
	var global map[string]any
	d.must(t, http.MethodGet, "/api/settings", nil, &global)
	if _, exists := global["agent"]; exists {
		t.Fatal("global settings contain an agent")
	}
	var agent apiAgent
	d.must(t, http.MethodGet, "/api/agents/default", nil, &agent)
	if agent.ID != "default" {
		t.Fatalf("default agent=%+v", agent)
	}
	t.Logf("artifact: removed routes return 404; global settings omit agent; explicit agent=%s", agent.ID)
}
