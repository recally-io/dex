package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/MichaelKinsy/PiG/durable"
)

func TestUserForkIsIdempotentAndChecksEntry(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), Home: t.TempDir()}
	a := coreEngine(t, cfg)
	ref := coreCreate(t, a, "alpha", coreOptions("echo"))
	coreWait(t, coreSubmit(t, a, ref, "first", "hello", "echo"))
	conv, err := a.conversation(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	var entry durable.EntryId
	_, err = conv.Commit(t.Context(), func(tx durable.Tx) (any, error) {
		id, err := tx.LatestEntryId(conv.Id())
		if err != nil {
			return nil, err
		}
		if id == nil {
			return nil, fmt.Errorf("entry missing")
		}
		entry = *id
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	opts := coreOptions("shout")
	if _, found, err := a.LookupFork(t.Context(), ref, "op", int64(entry), opts); err != nil || found {
		t.Fatalf("absent operation: found=%v err=%v", found, err)
	}
	results := make(chan Ref, 12)
	errs := make(chan error, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			child, err := a.Fork(t.Context(), ref, "op", int64(entry), opts)
			if err != nil {
				errs <- err
				return
			}
			results <- child
		})
	}
	wg.Wait()
	close(errs)
	close(results)
	for err := range errs {
		t.Error(err)
	}
	var child Ref
	for got := range results {
		if child.ID != 0 && got != child {
			t.Fatalf("duplicate child: %+v %+v", child, got)
		}
		child = got
	}
	if child.ID == 0 {
		t.Fatal("no child")
	}
	if saved, found, err := a.LookupFork(t.Context(), ref, "op", int64(entry), opts); err != nil || !found || saved != child {
		t.Fatalf("saved result=%+v found=%v err=%v", saved, found, err)
	}
	if _, err := a.Fork(t.Context(), ref, "op", int64(entry), coreOptions("echo")); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("different arguments: %v", err)
	}
	other := coreCreate(t, a, "alpha", coreOptions("echo"))
	if _, err := a.Fork(t.Context(), other, "op", int64(entry), opts); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign entry: %v", err)
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	a = coreEngine(t, cfg)
	got, err := a.Fork(t.Context(), ref, "op", int64(entry), opts)
	if err != nil || got != child {
		t.Fatalf("restart got=%+v err=%v", got, err)
	}
}

func TestUserForkUsesCurrentSettingsAndFreshRole(t *testing.T) {
	reports := make(chan TaskContext, 1)
	a := coreEngine(t, Config{Tasks: TaskTools{ReportTask: func(_ context.Context, source TaskContext, _ TaskReport) error { reports <- source; return nil }}})
	ref := coreCreate(t, a, "alpha", coreOptions("echo"))
	root, err := a.Submit(t.Context(), ref, Input{RequestID: "task-root", Content: "goal", Background: true, Options: coreOptions("echo")})
	if err != nil {
		t.Fatal(err)
	}
	coreWait(t, root)
	c, err := a.conversation(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	var at durable.EntryId
	_, err = c.Commit(t.Context(), func(tx durable.Tx) (any, error) {
		p, err := tx.LatestEntryId(c.Id())
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, fmt.Errorf("missing entry")
		}
		at = *p
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	current := coreOptions("shout")
	current.Cwd = t.TempDir()
	if err := c.Configure(t.Context(), agentChange(current)); err != nil {
		t.Fatal(err)
	}
	childRef, err := a.Fork(t.Context(), ref, "fork", int64(at), current)
	if err != nil {
		t.Fatal(err)
	}
	child, err := a.conversation(t.Context(), childRef)
	if err != nil {
		t.Fatal(err)
	}
	_, err = child.Commit(t.Context(), func(tx durable.Tx) (any, error) {
		tasks, err := tx.ScanTasks(durable.TaskQuery{ConversationId: new(child.Id())}, 1, nil)
		if err != nil {
			return nil, err
		}
		if len(tasks.Items) != 0 {
			return nil, fmt.Errorf("fork started execution")
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := child.Agent(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if actual.Model == nil || actual.Model.Provider != "faux" || actual.Model.ModelId != "shout" || actual.Cwd == nil || *actual.Cwd != current.Cwd || string(actual.ThinkingLevel) != current.ThinkingLevel {
		t.Fatalf("current config not inherited: %+v", actual)
	}
	freshRef := coreCreate(t, a, "alpha", current)
	fresh, err := a.conversation(t.Context(), freshRef)
	if err != nil {
		t.Fatal(err)
	}
	freshAgent, err := fresh.Agent(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	names := func(xs []*durable.Extension) []string {
		out := make([]string, 0, len(xs))
		for _, x := range xs {
			out = append(out, x.Name)
		}
		return out
	}
	if !slices.Equal(names(actual.Extensions), names(freshAgent.Extensions)) {
		t.Fatalf("fork extensions %v, fresh Chat %v", names(actual.Extensions), names(freshAgent.Extensions))
	}
	for _, list := range [][]*durable.ToolRegistration{actual.Tools, actual.Callable, actual.Deferred} {
		for _, tool := range list {
			if tool.Name == "report_task" {
				t.Fatal("fork inherited report_task")
			}
		}
	}
	_, err = child.Commit(t.Context(), func(tx durable.Tx) (any, error) {
		_, binding, err := bindingDraft(tx, child.Id())
		if err != nil {
			return nil, err
		}
		if binding.Root != "" || len(binding.Inputs) != 0 || binding.Sealed {
			return nil, fmt.Errorf("inherited task authority: %+v", binding)
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := a.Submit(t.Context(), childRef, Input{RequestID: "fork-input", Content: `report_task {"outcome":"done","summary":"wrong","cause":"","reproduction":[],"files":[],"openItems":[]}`, Options: coreOptions("tool")})
	if err != nil {
		t.Fatal(err)
	}
	coreWait(t, sub)
	select {
	case report := <-reports:
		t.Fatalf("fork invoked parent report authority: %+v", report)
	default:
	}
	t.Log("fork uses current config, fresh Chat extensions, and no parent task binding or report_task")
}
