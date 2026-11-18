package app_test

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"uuid"

	"github.com/recally-io/dex/internal/app"
)

func TestUserEditsPrivateEnvironmentWithoutReadingValues(t *testing.T) {
	t.Parallel()
	// Given an agent with a loaded environment.
	data := t.TempDir()
	d := startDexHome(t, app.Config{DataDir: data}, t.TempDir())
	d.setModel(t, "bash")
	initial := d.newChat(t, `test -z "${EDIT_SECRET+x}" && printf unloaded`)
	w := d.watch(t, initial)
	w.until(t, "initial environment", answered("bash: unloaded"))
	path := filepath.Join(data, "agents", "default", ".agents", ".env")
	const secret = "private-env-write-value"
	endpoint := "/api/agents/default/extensions/env"
	checkResponse := func(status int, body string, want int) {
		t.Helper()
		if status != want || strings.Contains(body, secret) {
			t.Fatalf("response status=%d, want=%d; value disclosed=%t", status, want, strings.Contains(body, secret))
		}
	}
	// When the user saves a value, the API returns only its key name.
	status, body := d.call(t, http.MethodPut, endpoint, map[string]string{"scope": "agent", "key": "EDIT_SECRET", "value": secret})
	checkResponse(status, body, http.StatusOK)
	if !strings.Contains(body, `"key":"EDIT_SECRET"`) {
		t.Fatal("saved key missing")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("file mode=%o", info.Mode().Perm())
	}
	info, err = os.Stat(filepath.Dir(path))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("directory mode: %v %v", info, err)
	}
	d.send(t, initial, `test -z "${EDIT_SECRET+x}" && printf still-unloaded`)
	w.until(t, "no automatic reload", answered("bash: still-unloaded"))
	status, body = d.reload(t)
	checkResponse(status, body, http.StatusOK)
	w = d.watch(t, d.newChat(t, fmt.Sprintf(`test "$(printf %%s "$EDIT_SECRET" | shasum -a 256 | cut -d " " -f 1)" = %x && printf loaded`, sha256.Sum256([]byte(secret)))))
	w.until(t, "saved environment after reload", answered("bash: loaded"))
	// Then replacements retain comments and the first position, and remove duplicates.
	writeFile(t, path, "# credentials\n\nexport EDIT_SECRET=old\nOTHER=keep\nEDIT_SECRET=duplicate\n# end\n")
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	status, body = d.call(t, http.MethodPut, endpoint, map[string]string{"scope": "agent", "key": "EDIT_SECRET", "value": secret})
	checkResponse(status, body, http.StatusOK)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "# credentials\n\nEDIT_SECRET="+secret+"\nOTHER=keep\n# end\n" {
		t.Fatal("replacement changed layout")
	}
	info, err = os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("replacement mode is not 0600")
	}
	status, body = d.call(t, http.MethodDelete, endpoint+"?scope=agent&key=EDIT_SECRET", nil)
	checkResponse(status, body, http.StatusOK)
	content, err = os.ReadFile(path)
	if err != nil || string(content) != "# credentials\n\nOTHER=keep\n# end\n" {
		t.Fatal("delete changed unrelated lines")
	}
	status, body = d.call(t, http.MethodGet, "/api/agents/default/extensions", nil)
	checkResponse(status, body, http.StatusOK)
	d.stop()
	t.Log("artifact: API responses contain no value; file mode 0600; Reload applies the value")
}

func TestUserSharesDexEnvironmentBetweenAgents(t *testing.T) {
	t.Parallel()
	// Given two agents that have already loaded their environments.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "bash")
	var second apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Second", "provider": "faux", "modelId": "bash", "thinkingLevel": "off"}, &second, http.StatusCreated)
	for _, id := range []string{"default", second.ID} {
		var main chatInfo
		d.must(t, http.MethodGet, "/api/agents/"+id+"/chats/main", nil, &main)
		d.must(t, http.MethodPost, "/api/agents/"+id+"/extensions/reload", nil, nil)
	}
	// When the user writes a shared key and reloads each agent.
	d.must(t, http.MethodPut, "/api/agents/default/extensions/env", map[string]string{"scope": "dex", "key": "SHARED_SECRET", "value": "shared-private-value"}, nil)
	for _, id := range []string{"default", second.ID} {
		d.must(t, http.MethodPost, "/api/agents/"+id+"/extensions/reload", nil, nil)
	}
	// Then both agents can use the value without placing it in a response.
	for _, id := range []string{"default", second.ID} {
		var main chatInfo
		base := "/api/agents/" + id
		d.must(t, http.MethodGet, base+"/chats/main", nil, &main)
		path := fmt.Sprintf("%s/chats/%d", base, main.ID)
		w := d.watchPath(t, path+"/events")
		d.must(t, http.MethodPost, path+"/messages", map[string]string{"id": uuid.NewV7().String(), "text": fmt.Sprintf(`test "$(printf %%s "$SHARED_SECRET" | shasum -a 256 | cut -d " " -f 1)" = %x && printf shared`, sha256.Sum256([]byte("shared-private-value")))}, nil, http.StatusAccepted)
		w.until(t, "shared environment", answered("bash: shared"))
	}
	t.Log("artifact: both agents use dex environment after Reload")
}
