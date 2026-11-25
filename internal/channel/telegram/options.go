package telegram

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/recally-io/dex/internal/speech"
	"github.com/recally-io/dex/internal/upload"
)

// Options are saved channel preferences. Speech requires a separate data-sending consent.
type Options struct {
	Silent    bool           `json:"silent"`
	Preview   string         `json:"preview,omitempty"`
	Reactions *bool          `json:"reactions,omitempty"`
	STT       speech.Options `json:"stt"`
	TTS       speech.Options `json:"tts"`
}

func (o Options) Validate() error {
	if !slices.Contains([]string{"", "auto", "edit", "draft", "off"}, o.Preview) {
		return errors.New("preview must be auto, edit, draft or off")
	}
	for _, item := range []struct {
		kind   string
		config speech.Options
	}{{"stt", o.STT}, {"tts", o.TTS}} {
		s := item.config
		if s.Enabled && (!s.AllowSending || strings.TrimSpace(s.Provider) == "" || strings.TrimSpace(s.Model) == "") {
			return fmt.Errorf("%s requires a provider, model and explicit permission to send audio/text to that provider", item.kind)
		}
		if s.Enabled && item.kind == "tts" && strings.TrimSpace(s.Voice) == "" {
			return errors.New("tts requires an explicit voice")
		}
		if s.TimeoutSeconds < 0 || s.TimeoutSeconds > 120 || s.MaxBytes < 0 || s.MaxBytes > upload.MaxSize || s.MaxDurationSeconds < 0 || s.MaxDurationSeconds > 3600 || s.MaxTextCharacters < 0 || s.MaxTextCharacters > 10000 {
			return fmt.Errorf("%s speech budget is out of range", item.kind)
		}
		if !slices.Contains([]string{"", "opus", "mp3", "wav"}, s.Format) {
			return errors.New("speech format must be opus, mp3 or wav")
		}
	}
	return nil
}
