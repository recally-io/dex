package task

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// ErrObjectConflict means the requested object link or wait does not belong to the Task.
var ErrObjectConflict = errors.New("object does not belong to the task")

// Object identifies one upstream object inside a source namespace.
type Object struct {
	Type string `json:"type"`
	Key  string `json:"key"`
}

// Event is one change normalized by a source adapter. Times are Unix milliseconds.
type Event struct {
	Namespace, ReceiptKey, ChangeKey, Type string
	Objects                                []Object
	Revision, Content, SourceURL           string
	AuthorLogin, AuthorAssociation         string
	OccurredAt                             int64
	// RecordOnly prevents an event from creating a Task, resolving a wait, or being held.
	RecordOnly bool
	// HoldUnmatched keeps an unmatched event for the owner. Without it Dex records and ignores the event.
	// Dex always holds a conflict.
	HoldUnmatched bool
}

// Receipt identifies the committed event and its routing result.
type Receipt struct {
	EventID, TaskID, Disposition string
	Duplicate                    bool
	ResolvedWaits                int64
	// Alerted is true when the event added a Task alert to Main.
	Alerted bool
	// Held is true when the event waits for the Owner on Board.
	Held bool
}

// Wait matches one type of change on a linked object after a strict time boundary.
// An empty revision accepts any revision. Alert sends Main a Task alert when the wait resolves.
type Wait struct {
	Namespace, Type, Revision string
	Object                    Object
	AfterAt                   int64
	Alert                     bool
}

func eventKey(value string) bool {
	return value != "" && len(value) <= 512 && strings.TrimSpace(value) == value
}

func validObject(o Object) bool { return eventKey(o.Type) && eventKey(o.Key) }

// LinkObject binds an object to one Task. Repeating the binding does not change it.
func (s *Service) LinkObject(ctx context.Context, botID, taskID, namespace string, object Object) error {
	if !eventKey(namespace) || !validObject(object) {
		return ErrInvalid
	}
	return s.write(ctx, func(l *Ledger, now int64) error {
		return bindObject(ctx, l, botID, taskID, namespace, object, nil, now)
	})
}

func linkObject(ctx context.Context, q *sqlc.Queries, botID, taskID, namespace string, object Object) error {
	n, err := q.LinkTaskObject(ctx, sqlc.LinkTaskObjectParams{BotID: botID, TaskID: taskID, Namespace: namespace, ObjectType: object.Type, ObjectKey: object.Key})
	if err == nil && n == 0 {
		return ErrObjectConflict
	}
	return err
}

// AddWait records a condition on an object already linked to an open Task.
func (s *Service) AddWait(ctx context.Context, botID, taskID string, wait Wait) error {
	if !eventKey(wait.Namespace) || !eventKey(wait.Type) || !validObject(wait.Object) || wait.AfterAt <= 0 || len(wait.Revision) > 512 {
		return ErrInvalid
	}
	return s.write(ctx, func(l *Ledger, now int64) error {
		if err := addWait(ctx, l.q, botID, taskID, wait); err != nil {
			return err
		}
		task, err := l.q.GetAssignedTask(ctx, sqlc.GetAssignedTaskParams{ID: taskID, BotID: botID})
		if err != nil {
			return err
		}
		return l.RecordAssociation(ctx, Owner, task, "Wait added for "+wait.Type, now)
	})
}

func addWait(ctx context.Context, q *sqlc.Queries, botID, taskID string, wait Wait) error {
	var alert int64
	if wait.Alert {
		alert = 1
	}
	n, err := q.AddTaskWait(ctx, sqlc.AddTaskWaitParams{BotID: botID, TaskID: taskID, Namespace: wait.Namespace, EventType: wait.Type, ObjectType: wait.Object.Type, ObjectKey: wait.Object.Key, Revision: wait.Revision, AfterAt: wait.AfterAt, Alert: alert})
	if err == nil && n == 0 {
		return ErrObjectConflict
	}
	return err
}

// BindObject saves an object link and its optional initial wait in one transaction.
func (s *Service) BindObject(ctx context.Context, botID, taskID, namespace string, object Object, wait *Wait) error {
	if !eventKey(namespace) || !validObject(object) || wait != nil && (wait.Namespace != namespace || wait.Object != object || !eventKey(wait.Type) || wait.AfterAt <= 0 || len(wait.Revision) > 512) {
		return ErrInvalid
	}
	return s.write(ctx, func(l *Ledger, now int64) error {
		if _, err := l.q.GetAssignedTask(ctx, sqlc.GetAssignedTaskParams{ID: taskID, BotID: botID}); err != nil {
			return err
		}
		return bindObject(ctx, l, botID, taskID, namespace, object, wait, now)
	})
}

func bindObject(ctx context.Context, ledger *Ledger, botID, taskID, namespace string, object Object, wait *Wait, now int64) error {
	q := ledger.q
	bound, err := q.HasTaskObject(ctx, sqlc.HasTaskObjectParams{Namespace: namespace, ObjectType: object.Type, ObjectKey: object.Key})
	if err != nil {
		return err
	}
	if err := linkObject(ctx, q, botID, taskID, namespace, object); err != nil {
		return err
	}
	if wait != nil {
		if err := addWait(ctx, q, botID, taskID, *wait); err != nil {
			return err
		}
	} else if bound != 0 {
		return nil
	}
	task, err := q.GetAssignedTask(ctx, sqlc.GetAssignedTaskParams{ID: taskID, BotID: botID})
	if err != nil {
		return err
	}
	return ledger.RecordAssociation(ctx, Owner, task, "Object linked: "+namespace+"/"+object.Type+"/"+object.Key, now)
}

// ListWaits returns the open and resolved waits for this Task.
// Accept commits the receipt, event, timeline entry, wait changes, and Task alert together.
// A successful return is safe to acknowledge. A conflict stays recorded but takes no action.
func (s *Service) Accept(ctx context.Context, event Event) (Receipt, error) {
	return s.accept(ctx, event, "", "")
}

// AcceptCreating links an unmatched event to a new Task under an explicitly selected bot.
// The event must provide exactly one creation reference. Existing links always win.
func (s *Service) AcceptCreating(ctx context.Context, event Event, botID, title string) (Receipt, error) {
	if botID == "" || title == "" || utf8.RuneCountInString(title) > 160 || len(event.Objects) != 1 {
		return Receipt{}, ErrInvalid
	}
	return s.accept(ctx, event, botID, title)
}

func (s *Service) accept(ctx context.Context, event Event, botID, title string) (Receipt, error) {
	if !eventKey(event.Namespace) || !eventKey(event.ReceiptKey) || !eventKey(event.Type) ||
		event.ChangeKey != "" && !eventKey(event.ChangeKey) || event.OccurredAt <= 0 ||
		len(event.Objects) > 16 || len(event.Revision) > 512 || len(event.SourceURL) > 4096 ||
		utf8.RuneCountInString(event.Content) > 32000 {
		return Receipt{}, ErrInvalid
	}
	for _, object := range event.Objects {
		if !validObject(object) {
			return Receipt{}, ErrInvalid
		}
	}
	refs, err := json.Marshal(event.Objects)
	if err != nil {
		return Receipt{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Receipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	ledger := NewLedger(q)
	id := uuid.NewV7().String()
	// The receipt insert takes the SQLite writer before we inspect the routing state.
	n, err := q.ClaimTaskSourceReceipt(ctx, sqlc.ClaimTaskSourceReceiptParams{Namespace: event.Namespace, ReceiptKey: event.ReceiptKey, EventID: id})
	if err != nil {
		return Receipt{}, err
	}
	if n == 0 {
		saved, err := q.GetTaskSourceEventByReceipt(ctx, sqlc.GetTaskSourceEventByReceiptParams{Namespace: event.Namespace, ReceiptKey: event.ReceiptKey})
		return duplicateReceipt(saved), err
	}
	change := sql.NullString{String: event.ChangeKey, Valid: event.ChangeKey != ""}
	if change.Valid {
		saved, err := q.GetTaskSourceEventByChange(ctx, sqlc.GetTaskSourceEventByChangeParams{Namespace: event.Namespace, ChangeKey: change})
		if err == nil {
			if err := q.SetTaskSourceReceiptEvent(ctx, sqlc.SetTaskSourceReceiptEventParams{EventID: saved.ID, Namespace: event.Namespace, ReceiptKey: event.ReceiptKey}); err != nil {
				return Receipt{}, err
			}
			return duplicateReceipt(saved), tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Receipt{}, err
		}
	}
	task, disposition, err := findEventTask(ctx, q, event)
	if err != nil {
		return Receipt{}, err
	}
	now := time.Now().UnixMilli()
	created := false
	if disposition == "unmatched" && botID != "" && !event.RecordOnly {
		task, err = createEventTask(ctx, ledger, event, botID, title, now)
		if err != nil {
			return Receipt{}, err
		}
		if task.ID != "" {
			disposition, created = "attached", true
		}
	}
	row := sqlc.StoreTaskSourceEventParams{
		ID: id, Namespace: event.Namespace, ChangeKey: change, EventType: event.Type, Objects: string(refs),
		Revision: event.Revision, Content: event.Content, SourceUrl: event.SourceURL, OccurredAt: event.OccurredAt, ReceivedAt: now,
		AuthorLogin: event.AuthorLogin, AuthorAssociation: event.AuthorAssociation,
		TaskID: sql.NullString{String: task.ID, Valid: task.ID != ""}, Disposition: disposition,
	}
	if err := q.StoreTaskSourceEvent(ctx, row); err != nil {
		return Receipt{}, err
	}
	receipt := Receipt{EventID: id, TaskID: task.ID, Disposition: disposition}
	if task.ID == "" {
		// An outsider can send many unmatched events, so only a trusted author can create a hold.
		receipt.Held = disposition == "conflict" || event.HoldUnmatched && !event.RecordOnly
		if receipt.Held {
			if err := q.HoldTaskSourceEvent(ctx, sqlc.HoldTaskSourceEventParams{EventID: id, HeldAt: now}); err != nil {
				return Receipt{}, err
			}
		}
		return receipt, tx.Commit()
	}
	resolved, alertWaits, err := applyEvent(ctx, ledger, task, event, id, now)
	if err != nil {
		return Receipt{}, err
	}
	receipt.ResolvedWaits = resolved
	var reason string
	// A record-only event and an event on a closed Task resolve no wait, so they never alert.
	switch {
	case created:
		reason = WakeAssigned
	case alertWaits > 0:
		reason = WakeGateUnblocked
	}
	if reason != "" {
		receipt.Alerted, err = ledger.queueWake(ctx, task.AssigneeBotID, task, reason, now)
		if err != nil {
			return Receipt{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Receipt{}, err
	}
	s.Notify(ledger)
	return receipt, nil
}

func createEventTask(ctx context.Context, ledger *Ledger, event Event, botID, title string, now int64) (sqlc.Task, error) {
	q := ledger.q
	object := event.Objects[0]
	// A link to an archived Task still owns the reference. Do not take it over.
	bound, err := q.HasTaskObject(ctx, sqlc.HasTaskObjectParams{Namespace: event.Namespace, ObjectType: object.Type, ObjectKey: object.Key})
	if err != nil || bound != 0 {
		return sqlc.Task{}, err
	}
	task, err := ledger.createProjection(ctx, sqlc.CreateTaskParams{ID: uuid.NewV7().String(), BotID: botID, Title: title, At: now,
		Status: StatusReady, CreatorKind: CreatorSource, CreatorID: event.Namespace})
	if errors.Is(err, sql.ErrNoRows) {
		return sqlc.Task{}, nil
	}
	if err != nil {
		return sqlc.Task{}, err
	}
	n, err := q.LinkTaskObject(ctx, sqlc.LinkTaskObjectParams{BotID: botID, TaskID: task.ID, Namespace: event.Namespace, ObjectType: object.Type, ObjectKey: object.Key})
	if err == nil && n == 0 {
		return sqlc.Task{}, ErrObjectConflict
	}
	return task, err
}

func duplicateReceipt(event sqlc.TaskSourceEvent) Receipt {
	return Receipt{EventID: event.ID, TaskID: event.TaskID.String, Disposition: event.Disposition, Duplicate: true}
}

func findEventTask(ctx context.Context, q *sqlc.Queries, event Event) (sqlc.Task, string, error) {
	var task sqlc.Task
	for _, object := range event.Objects {
		match, err := q.FindTaskByObject(ctx, sqlc.FindTaskByObjectParams{Namespace: event.Namespace, ObjectType: object.Type, ObjectKey: object.Key})
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return sqlc.Task{}, "", err
		}
		if task.ID != "" && task.ID != match.ID {
			return sqlc.Task{}, "conflict", nil
		}
		task = match
	}
	if task.ID == "" {
		return task, "unmatched", nil
	}
	return task, "attached", nil
}

// applyEvent returns the number of resolved waits and how many of them alert Main.
func applyEvent(ctx context.Context, ledger *Ledger, task sqlc.Task, event Event, id string, now int64) (resolved, alerts int64, err error) {
	if err := ledger.recordExternal(ctx, task, event.Content, id, now); err != nil {
		return 0, 0, err
	}
	if !IsOpenStatus(task.Status) || event.RecordOnly {
		return 0, 0, nil
	}
	for _, object := range event.Objects {
		waits, err := ledger.q.ResolveTaskWaits(ctx, sqlc.ResolveTaskWaitsParams{
			EventID: sql.NullString{String: id, Valid: true}, TaskID: task.ID, Namespace: event.Namespace,
			ObjectType: object.Type, ObjectKey: object.Key, EventType: event.Type, OccurredAt: event.OccurredAt, EventRevision: event.Revision,
		})
		if err != nil {
			return 0, 0, err
		}
		resolved += int64(len(waits))
		for _, alert := range waits {
			alerts += alert
		}
	}
	return resolved, alerts, nil
}

// Link is an object that a bot tool links to a Task, with an optional wait on it.
type Link struct {
	Namespace string
	Object    Object
	// WaitEvent and WaitRevision add a wait that starts at the call time. An empty WaitEvent adds no wait.
	WaitEvent, WaitRevision string
	Alert                   bool
	// Name names the object in error messages, for example "PR 7 of repository 10".
	Name string
}

// LinkTool is a bot tool that a source adds to link its objects to Tasks. The core adds the
// Task id argument, checks the caller, and writes the link with its replay record.
type LinkTool struct {
	Name, Description string
	// Rules is a paragraph for the system prompt that tells the bot when to use the tool.
	Rules      string
	Properties map[string]any
	Required   []string
	// Parse validates the JSON arguments. Its error tells the model what to fix.
	Parse func(args []byte) (Link, error)
}
