package engine

import (
	"context"

	"github.com/MichaelKinsy/PiG/durable"
)

// TaskReport records the worker's claim and the evidence behind it.
type TaskReport struct {
	Outcome      string        `json:"outcome"`
	Summary      string        `json:"summary"`
	Cause        string        `json:"cause"`
	Reproduction []TaskCommand `json:"reproduction"`
	Files        []string      `json:"files"`
	OpenItems    []string      `json:"openItems"`
}

type TaskCommand struct {
	Command string `json:"command"`
	Ran     bool   `json:"ran"`
}

func taskReportExtension(agentID string, report func(context.Context, TaskContext, TaskReport) error) *durable.Extension {
	rules := "Before this background task ends, call report_task once. Use outcome done only when the saved goal and every done criterion are met. Use blocked when input or a prerequisite is missing, or failed when the work failed. Include the summary, cause, reproduction commands with an explicit ran boolean, files, and openItems. Never claim an unrun command passed. The report is provisional until execution ends. Missing reports cannot establish goal completion. Tools, including bash and coding tools, remain available. Task constraints are instructions; they are not a sandbox."
	strings := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	return stagedReportExtension(agentID, "task-report", "report_task", "Task report saved.", rules, "Save the structured result of this background task. The execution must still finish before the report is settled.", map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"outcome", "summary", "cause", "reproduction", "files", "openItems"}, "properties": map[string]any{
			"outcome":      map[string]any{"type": "string", "enum": []string{"done", "blocked", "failed"}},
			"summary":      map[string]any{"type": "string", "minLength": 1},
			"cause":        map[string]any{"type": "string"},
			"reproduction": map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"command", "ran"}, "properties": map[string]any{"command": map[string]any{"type": "string", "minLength": 1}, "ran": map[string]any{"type": "boolean"}}}},
			"files":        strings, "openItems": strings,
		}}, report)
}
