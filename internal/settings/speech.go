package settings

import (
	"context"
	"errors"
	"strings"

	"github.com/recally-io/dex/internal/speech"
)

// SpeechEndpoint reuses a saved provider, but never assumes its model supports audio.
func (s *Service) SpeechEndpoint(ctx context.Context, id string) (speech.Endpoint, error) {
	row, err := s.q.GetProvider(ctx, id)
	if err != nil {
		return speech.Endpoint{}, errors.New("speech provider is not configured")
	}
	if row.BaseUrl == "" || row.Api == "anthropic-messages" {
		return speech.Endpoint{}, errors.New("speech needs an explicit OpenAI-compatible provider base URL")
	}
	key, err := s.catalog.ProviderAPIKey(ctx, id)
	if err != nil {
		return speech.Endpoint{}, errors.New("speech provider credentials are unavailable")
	}
	if strings.HasPrefix(key, "op://") {
		if s.agents == nil {
			return speech.Endpoint{}, errors.New("speech provider credential references are unavailable")
		}
		key, err = s.agents.ResolveProviderAPIKey(ctx, key)
		if err != nil {
			return speech.Endpoint{}, errors.New("speech provider credentials are unavailable")
		}
	}
	return speech.Endpoint{URL: row.BaseUrl, APIKey: key}, nil
}
