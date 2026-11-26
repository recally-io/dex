package task

import (
	"errors"
	"strconv"
	"strings"
)

const (
	StatusBacklog    = "backlog"
	StatusReady      = "ready"
	StatusInProgress = "in_progress"
	StatusInReview   = "in_review"
	StatusDone       = "done"
	StatusCancelled  = "cancelled"

	KindNote      = "note"
	KindArtifact  = "artifact"
	KindStatus    = "status"
	KindAssign    = "assign"
	KindReview    = "review"
	KindExecution = "execution"
	KindExternal  = "external"

	TagBrief    = "brief"
	TagDecision = "decision"
	TagHandoff  = "handoff"
	TagUpdate   = "update"

	ActorOwner         = "owner"
	ActorBot           = "bot"
	ActorExternalAgent = "external_agent"
	ActorSystem        = "system"
	ActorExternal      = "external"
	CreatorSource      = "source"

	TrustOwner    = "owner"
	TrustAgent    = "agent"
	TrustExternal = "external"

	ReviewApprove        = "approve"
	ReviewRequestChanges = "request_changes"
)

var (
	ErrInvalid   = errors.New("invalid task command")
	ErrConflict  = errors.New("task changed")
	ErrForbidden = errors.New("task action not allowed")
)

func FormatKey(number int64) string { return "T-" + strconv.FormatInt(number, 10) }

func ParseKey(s string) (int64, bool) {
	digits, ok := strings.CutPrefix(s, "T-")
	if !ok || digits == "" || digits[0] < '1' || digits[0] > '9' {
		return 0, false
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func IsOpenStatus(status string) bool {
	switch status {
	case StatusBacklog, StatusReady, StatusInProgress, StatusInReview:
		return true
	}
	return false
}
