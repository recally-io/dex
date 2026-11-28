package bridge

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/localdex"
)

func TestUserLocalWorkspaceServesUIAndFiles(t *testing.T) {
	t.Parallel()
	bridge := startTestBridge(t)
	page, err := bridge.serveStatic(t.Context(), http.MethodGet, AppleOrigin+"/", nil)
	if err != nil || page.Status != http.StatusOK || !bytes.Contains(page.Body, []byte(`id="root"`)) {
		t.Fatalf("open workspace: status %d, error %v", page.Status, err)
	}
	asset := regexp.MustCompile(`src="(/assets/[^" ]+\.js)"`).FindSubmatch(page.Body)
	if len(asset) != 2 {
		t.Fatal("workspace did not include its script")
	}
	android, err := bridge.serveStatic(t.Context(), http.MethodGet, localAndroidOrigin+"/", nil)
	shim, module := bytes.Index(android.Body, []byte(localBridgeJS)), bytes.Index(android.Body, asset[0])
	if err != nil || android.Status != http.StatusOK || shim < 0 || module < shim || bytes.Contains(page.Body, []byte(localBridgeJS)) {
		t.Fatalf("Android must run the shim before the app; Apple uses WKUserScript (status %d, error %v)", android.Status, err)
	}
	js, err := bridge.serveStatic(t.Context(), http.MethodGet, localAndroidOrigin+string(asset[1]), nil)
	if err != nil || js.Status != http.StatusOK || !strings.Contains(js.Headers.Get("Content-Type"), "javascript") || len(js.Body) == 0 {
		t.Fatalf("load script: status %d, headers %v, error %v", js.Status, js.Headers, err)
	}

	bot := localTestBot(t, bridge)
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", "test.bin")
	if err != nil {
		t.Fatal(err)
	}
	data := []byte{0, 0xff, 1, 0x80}
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	path := "/api/bots/" + bot + "/uploads"
	result := fetchLocal(t, bridge, http.MethodPost, path, body.Bytes(), form.FormDataContentType(), http.StatusCreated)
	var upload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(result.Body, &upload); err != nil || upload.ID == "" {
		t.Fatalf("upload response: %s, %v", result.Body, err)
	}
	got, err := bridge.serveStatic(t.Context(), http.MethodGet, path+"/"+upload.ID, nil)
	if err != nil || got.Status != http.StatusOK || !bytes.Equal(got.Body, data) {
		t.Fatalf("download upload: status %d, body %q, error %v", got.Status, got.Body, err)
	}
	fetchLocal(t, bridge, http.MethodDelete, "/api/bots/"+bot+"/channels/telegram", nil, "", http.StatusOK)
}

func TestUserEditsLocalWorkspaceAndReceivesEvents(t *testing.T) {
	t.Parallel()
	bridge := startTestBridge(t)
	bot := localTestBot(t, bridge)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	chunks, ended := make(chan string, 8), make(chan error, 1)
	if err := bridge.openSSE(ctx, "tasks", "/api/tasks/events", "0", func(chunk []byte) {
		select {
		case chunks <- string(chunk):
		case <-ctx.Done():
		}
	}, func(err error) { ended <- err }); err != nil {
		t.Fatal(err)
	}
	defer bridge.closeSSE("tasks")
	wait := func(want string) {
		t.Helper()
		for {
			select {
			case got := <-chunks:
				if strings.Contains(got, want) {
					return
				}
			case err := <-ended:
				t.Fatalf("stream ended before %q: %v", want, err)
			case <-ctx.Done():
				t.Fatalf("waiting for %q: %v", want, ctx.Err())
			}
		}
	}
	wait("event: task_ready\n")
	task := fetchLocal(t, bridge, http.MethodPost, "/api/tasks", []byte(`{"title":"First title","assigneeBotId":"`+bot+`"}`), "application/json", http.StatusCreated)
	wait("event: task_entry\n")
	var created struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(task.Body, &created); err != nil || created.Key == "" {
		t.Fatalf("create task: %s, %v", task.Body, err)
	}
	patch := fetchLocal(t, bridge, http.MethodPatch, "/api/tasks/"+created.Key, []byte(`{"title":"New title"}`), "application/json", http.StatusOK)
	if !bytes.Contains(patch.Body, []byte(`"New title"`)) {
		t.Fatalf("patch task: %s", patch.Body)
	}
	bridge.closeSSE("tasks")
	select {
	case err := <-ended:
		if err != nil || ctx.Err() != nil {
			t.Fatalf("stream did not stop on explicit close: %v (parent: %v)", err, ctx.Err())
		}
	case <-ctx.Done():
		t.Fatal("stream did not stop on explicit close")
	}
}

func startTestBridge(t *testing.T) *Bridge {
	t.Helper()
	instance, err := localdex.Start(t.Context(), localdex.Config{DataDir: t.TempDir(), Mobile: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := instance.Close(); err != nil {
			t.Error(err)
		}
	})
	return New(instance.Handler())
}

func fetchLocal(t *testing.T, bridge *Bridge, method, path string, body []byte, contentType string, want int) Response {
	t.Helper()
	response, err := bridge.fetch(t.Context(), localRequest{Method: method, URL: path, Body: body, Header: http.Header{
		"Content-Type": {contentType}, "Origin": {"null"}, "Sec-Fetch-Site": {"cross-site"},
	}})
	if err != nil || response.Status != want {
		t.Fatalf("%s %s: status %d, want %d; body %s; %v", method, path, response.Status, want, response.Body, err)
	}
	return response
}

func localTestBot(t *testing.T, bridge *Bridge) string {
	t.Helper()
	result := fetchLocal(t, bridge, http.MethodGet, "/api/bots", nil, "", http.StatusOK)
	var list struct {
		Bots []struct {
			ID string `json:"id"`
		} `json:"bots"`
	}
	if err := json.Unmarshal(result.Body, &list); err != nil || len(list.Bots) != 1 || list.Bots[0].ID == "" {
		t.Fatalf("list bots: %s, %v", result.Body, err)
	}
	return list.Bots[0].ID
}
