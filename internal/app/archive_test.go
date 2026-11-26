package app_test

import (
	"fmt"
	"net/http"
	"testing"
)

type archivedInfo struct {
	ID         int64  `json:"id"`
	ArchivedAt *int64 `json:"archivedAt"`
}

func (d *dex) archive(t *testing.T, method string, sessionID int64, want int) archivedInfo {
	t.Helper()
	var c archivedInfo
	var out any = &c
	if want != http.StatusOK {
		out = nil
	}
	d.must(t, method, fmt.Sprintf("/api/agents/default/sessions/%d/archive", sessionID), nil, out, want)
	return c
}

func listed(t *testing.T, d *dex, id int64) (archivedInfo, bool) {
	t.Helper()
	var list struct {
		Sessions []archivedInfo `json:"sessions"`
	}
	d.must(t, http.MethodGet, "/api/agents/default/sessions", nil, &list)
	for _, c := range list.Sessions {
		if c.ID == id {
			return c, true
		}
	}
	return archivedInfo{}, false
}

func TestUserArchivesAndRestoresSessionSession(t *testing.T) {
	t.Parallel()
	// Given a bot with Main and a session that has a message.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	main := d.mainSession(t)
	session := d.newSessionWithMessage(t, "keep this")

	// When the user archives the Session, it keeps its data and carries the archive time.
	archived := d.archive(t, http.MethodPut, session, http.StatusOK)
	if archived.ID != session || archived.ArchivedAt == nil || *archived.ArchivedAt <= 0 {
		t.Fatalf("archived session: %+v", archived)
	}
	var got archivedInfo
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/sessions/%d", session), nil, &got)
	if got.ArchivedAt == nil || *got.ArchivedAt != *archived.ArchivedAt {
		t.Fatalf("session after archive: %+v", got)
	}
	if c, ok := listed(t, d, session); !ok || c.ArchivedAt == nil {
		t.Fatalf("archived session left the list or lost its archive time: %+v %v", c, ok)
	}
	// Archiving again keeps the first time.
	if again := d.archive(t, http.MethodPut, session, http.StatusOK); *again.ArchivedAt != *archived.ArchivedAt {
		t.Fatalf("second archive changed the time: %+v", again)
	}

	// When the user restores it, the archive time is gone.
	if restored := d.archive(t, http.MethodDelete, session, http.StatusOK); restored.ArchivedAt != nil {
		t.Fatalf("restored session: %+v", restored)
	}
	if c, ok := listed(t, d, session); !ok || c.ArchivedAt != nil {
		t.Fatalf("restored session in list: %+v %v", c, ok)
	}

	// Main and the read-only kinds cannot be archived; an unknown session is not found.
	d.archive(t, http.MethodPut, main, http.StatusForbidden)
	for _, key := range []string{"task:archive", "automation:archive", "telegram:archive"} {
		d.archive(t, http.MethodPut, sessionFixture(t, d, key, 1), http.StatusForbidden)
	}
	d.archive(t, http.MethodPut, 999999, http.StatusNotFound)
}

func TestUserNewSessionDoesNotReuseArchivedEmptySession(t *testing.T) {
	t.Parallel()
	// Given an empty session that the user archived.
	d := startDex(t, t.TempDir(), "")
	empty := d.newSession(t)
	d.archive(t, http.MethodPut, empty.ID, http.StatusOK)

	// When the user opens a new session, dex creates another one instead of the archived one.
	if fresh := d.newSession(t); fresh.ID == empty.ID {
		t.Fatalf("New session reused the archived session %d", empty.ID)
	}
}
