package telegram

import (
	"errors"
	"strings"

	"github.com/recally-io/dex/internal/speech"
	"github.com/recally-io/dex/internal/upload"
)

// Options are saved channel preferences. Speech requires a separate data-sending consent.
type Options struct {
	Silent    bool           `json:"silent"`
	Typing    *bool          `json:"typing,omitempty"`
	Reactions *bool          `json:"reactions,omitempty"`
	STT       speech.Options `json:"stt"`
}

func (o Options) Validate() error {
	s := o.STT
	if s.Enabled && (!s.AllowSending || strings.TrimSpace(s.Provider) == "" || strings.TrimSpace(s.Model) == "") {
		return errors.New("stt requires a provider, model and explicit permission to send audio to that provider")
	}
	if s.TimeoutSeconds < 0 || s.TimeoutSeconds > 120 || s.MaxBytes < 0 || s.MaxBytes > upload.MaxSize || s.MaxDurationSeconds < 0 || s.MaxDurationSeconds > 3600 {
		return errors.New("stt speech budget is out of range")
	}
	return nil
}
