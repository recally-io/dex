package task

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserDecidesHeldEventsInTheInbox(t *testing.T) {
	ctx := t.Context()
	s, _, woken := openEventStore(t, filepath.Join(t.TempDir(), "control.sqlite"))
	if _, err := s.q.CreateAgent(ctx, sqlc.CreateAgentParams{ID: "default", Name: "Dex", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	review, err := s.CreateTask(ctx, Owner, OriginUser, CreateTaskParams{AssigneeBotID: "default", Title: "Review the change"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateTask(ctx, Owner, OriginUser, CreateTaskParams{AssigneeBotID: "default", Title: "Other work"})
	if err != nil {
		t.Fatal(err)
	}
	const namespace = "test/account"
	linked, elsewhere, free := Object{Type: "proposal", Key: "1"}, Object{Type: "proposal", Key: "2"}, Object{Type: "proposal", Key: "3"}
	if err := s.LinkObject(ctx, "default", review.ID, namespace, linked); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkObject(ctx, "default", other.ID, namespace, elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := s.AddWait(ctx, "default", review.ID, Wait{Namespace: namespace, Type: "review", Object: linked, AfterAt: 1, Alert: true}); err != nil {
		t.Fatal(err)
	}
	event := func(key string, objects ...Object) Event {
		return Event{Namespace: namespace, ReceiptKey: key, ChangeKey: key, Type: "review", Objects: objects, Content: "Text " + key, OccurredAt: 10}
	}

	cases := []struct {
		name string
		edit func(*Event)
		held bool
	}{
		{"an ignored unmatched event", func(*Event) {}, false},
		{"a held unmatched event", func(e *Event) { e.HoldUnmatched = true }, true},
		{"an unmatched event from an outsider", func(e *Event) { e.HoldUnmatched, e.RecordOnly = true, true }, false},
	}
	held := map[string]string{}
	for _, c := range cases {
		e := event(c.name, free)
		c.edit(&e)
		got, err := s.Accept(ctx, e)
		if err != nil || got.Disposition != "unmatched" || got.Held != c.held {
			t.Fatalf("%s: %+v, %v", c.name, got, err)
		}
		if c.held {
			held[c.name] = got.EventID
		}
	}
	conflict, err := s.Accept(ctx, event("conflict", linked, elsewhere))
	if err != nil || conflict.Disposition != "conflict" || !conflict.Held || conflict.ResolvedWaits != 0 {
		t.Fatalf("Dex must hold a conflict: %+v, %v", conflict, err)
	}
	if replay, err := s.Accept(ctx, event("conflict", linked, elsewhere)); err != nil || !replay.Duplicate || replay.Held {
		t.Fatalf("a replay must not hold again: %+v, %v", replay, err)
	}
	listed, err := s.ListHeld(ctx)
	if err != nil || len(listed) != 2 || listed[0].ID != conflict.EventID && listed[1].ID != conflict.EventID {
		t.Fatalf("Board must list the two held events: %+v, %v", listed, err)
	}

	closed, err := s.CreateTask(ctx, Owner, OriginUser, CreateTaskParams{AssigneeBotID: "default", Title: "Done"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTask(ctx, closed.ID, UpdateParams{Status: new(StatusCancelled)}); err != nil {
		t.Fatal(err)
	}
	unmatched := held["a held unmatched event"]
	if err := s.AttachHeld(ctx, unmatched, "default", closed.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a closed Task must refuse a held event: %v", err)
	}
	if err := s.AttachHeld(ctx, unmatched, "default", review.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachHeld(ctx, unmatched, "default", review.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a decided event must not attach again: %v", err)
	}
	if err := s.AttachHeld(ctx, conflict.EventID, "default", review.ID); err != nil {
		t.Fatal(err)
	}
	detail, err := s.Detail(ctx, review.ID)
	if err != nil {
		t.Fatal(err)
	}
	waits := detail.Waits
	if len(waits) != 1 || waits[0].ResolvedBy.Valid || len(*woken) != 0 {
		t.Fatalf("an owner decision must not resolve a wait or alert Main: %+v, woken %v", waits, *woken)
	}
	if len(detail.Entries) != 5 || detail.Entries[3].EventID.String != unmatched || detail.Entries[4].EventID.String != conflict.EventID {
		t.Fatalf("the Task timeline must show both events: %+v", detail.Entries)
	}
	// The attach linked the free object, so its next event routes to the Task without the owner.
	later, err := s.Accept(ctx, event("later", free))
	if err != nil || later.Disposition != "attached" || later.TaskID != review.ID || later.Held {
		t.Fatalf("a later event must route to the Task: %+v, %v", later, err)
	}
	if owner, err := s.q.FindTaskByObject(ctx, sqlc.FindTaskByObjectParams{Namespace: namespace, ObjectType: elsewhere.Type, ObjectKey: elsewhere.Key}); err != nil || owner.ID != other.ID {
		t.Fatalf("an attach must keep the link of another Task: %+v, %v", owner, err)
	}
	if listed, err := s.ListHeld(ctx); err != nil || len(listed) != 0 {
		t.Fatalf("decided events must leave Board: %+v, %v", listed, err)
	}

	dismissed, err := s.Accept(ctx, event("dismissed", linked, elsewhere))
	if err != nil || !dismissed.Held {
		t.Fatalf("conflict: %+v, %v", dismissed, err)
	}
	if err := s.DismissHeld(ctx, dismissed.EventID); err != nil {
		t.Fatal(err)
	}
	if err := s.DismissHeld(ctx, dismissed.EventID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a dismissed event must not dismiss again: %v", err)
	}
	if listed, err := s.ListHeld(ctx); err != nil || len(listed) != 0 {
		t.Fatalf("a dismissed event must leave Board: %+v, %v", listed, err)
	}
}
