package engine

import (
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

// continuationExtensions removes every delegated-work extension from a finished task's conversation.
// Extension edits persist on the conversation, so a plain input would keep report_task. A task that
// was cancelled before its first input never had the edit, so the set removes the defaults too.
// Removing an extension that is absent changes nothing.
func continuationExtensions(agentID string, config Config) harness.Change[harness.ExtensionChange] {
	return harness.SetTo(harness.ExtensionChange{Edit: &harness.ExtensionEdit{Remove: []*durable.Extension{
		taskExtension(agentID, config.Tasks), workLoopExtension(), routineExtension(agentID, config.Routines), taskReportExtension(agentID, config.Tasks.ReportTask),
	}}})
}
