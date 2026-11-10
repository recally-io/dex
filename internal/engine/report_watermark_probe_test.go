package engine

import (
	"context"
	"fmt"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

// This probe records what PiG retains while report_task is executing. It does
// not assert that an earlier transcript entry was necessarily in the request.
func TestUserReportProducerCheckpointDoesNotRetainRequestCutoff(t *testing.T) {
	var a *Agents
	observed := make(chan error, 1)
	a = coreEngine(t, Config{Tasks: TaskTools{ReportTask: func(ctx context.Context, source TaskContext, _ TaskReport) error {
		conv, err := a.conversation(ctx, Ref{AgentID: source.AgentID, ID: source.ConversationID})
		if err != nil {
			observed <- err
			return err
		}
		_, err = conv.Commit(ctx, func(tx durable.Tx) (any, error) {
			page, err := tx.ScanTasks(durable.TaskQuery{ConversationId: new(conv.Id())}, 100, nil)
			if err != nil {
				return nil, err
			}
			for _, tool := range page.Items {
				input, err := durable.FromJsonValue[harness.ToolTaskInput](tool.Input)
				if err != nil || input.Kind != harness.ToolTaskModel || input.CallId == "" || tool.Owner == nil {
					continue
				}
				entry, err := tx.Entry(input.Assistant)
				if err != nil {
					return nil, err
				}
				if entry == nil {
					return nil, fmt.Errorf("assistant entry missing")
				}
				isReport := false
				for _, msg := range entry.Model {
					if assistant, ok := msg.(ai.AssistantMessage); ok {
						for _, block := range assistant.Content {
							if call, ok := block.(ai.ToolCall); ok && call.ID == input.CallId && call.Name == "report_task" {
								isReport = true
							}
						}
					}
				}
				if !isReport {
					continue
				}
				owner, err := tx.Task(*tool.Owner)
				if err != nil {
					return nil, err
				}
				if owner == nil || owner.State.Checkpoint == nil {
					return nil, fmt.Errorf("generation checkpoint missing")
				}
				checkpoint, err := durable.FromJsonValue[harness.GenerationCheckpoint](*owner.State.Checkpoint)
				if err != nil {
					return nil, err
				}
				if checkpoint.Assistant == nil || *checkpoint.Assistant != input.Assistant || checkpoint.Cutoff != nil {
					return nil, fmt.Errorf("unexpected owner checkpoint: %+v", checkpoint)
				}
				t.Logf("tool=%d generation=%d assistant=%d phase=%s cutoff=nil", tool.Id, owner.Id, input.Assistant, checkpoint.Phase)
				return nil, nil
			}
			return nil, fmt.Errorf("model tool task not found")
		})
		observed <- err
		return err
	}}})
	ref := coreCreate(t, a, "alpha", coreOptions("tool"))
	sub, err := a.Submit(t.Context(), ref, Input{RequestID: "root", Content: `report_task {"outcome":"done","summary":"ok","cause":"","reproduction":[],"files":[],"openItems":[]}`, Background: true, Options: coreOptions("tool")})
	if err != nil {
		t.Fatal(err)
	}
	coreWait(t, sub)
	if err := <-observed; err != nil {
		t.Fatal(err)
	}
}
