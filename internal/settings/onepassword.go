package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// ToolSettings receives the settings that tools use. *engine.Agents implements it.
type ToolSettings interface {
	SetOnePassword(engine.OnePassword)
	OnePasswordCLI(ctx context.Context) string
}

// OnePassword is the 1Password view for the web UI. Tokens are write-only: requests send them,
// responses report only whether one is saved.
type OnePassword struct {
	// Mode is "off", "connect", or "service-account".
	Mode                   string
	ConnectHost            string
	ConnectTokenSet        bool
	ServiceAccountTokenSet bool
	// CLI is the path of op in the PATH of the tool environment, or "".
	CLI string
}

// OnePasswordInput changes the settings. An empty token keeps the saved token.
type OnePasswordInput struct {
	Mode, ConnectHost, ConnectToken, ServiceAccountToken string
}

func (s *Service) storedOnePassword(ctx context.Context) (sqlc.GetOnePasswordRow, error) {
	row, err := s.q.GetOnePassword(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return sqlc.GetOnePasswordRow{}, nil
	}
	return row, err
}

// OnePassword returns the settings without tokens.
func (s *Service) OnePassword(ctx context.Context) (OnePassword, error) {
	row, err := s.storedOnePassword(ctx)
	if err != nil {
		return OnePassword{}, err
	}
	mode := row.Mode
	if mode == "" {
		mode = "off"
	}
	return OnePassword{Mode: mode, ConnectHost: row.ConnectHost, ConnectTokenSet: row.ConnectToken != "", ServiceAccountTokenSet: row.ServiceAccountToken != "", CLI: s.agents.OnePasswordCLI(ctx)}, nil
}

// SetOnePassword saves the settings and gives them to tools. Open agents apply them at their
// next automatic reload.
func (s *Service) SetOnePassword(ctx context.Context, in OnePasswordInput) (OnePassword, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := s.storedOnePassword(ctx)
	if err != nil {
		return OnePassword{}, err
	}
	next := sqlc.SetOnePasswordParams{
		Mode: in.Mode, ConnectHost: strings.TrimSpace(in.ConnectHost),
		ConnectToken: row.ConnectToken, ServiceAccountToken: row.ServiceAccountToken,
	}
	if token := strings.TrimSpace(in.ConnectToken); token != "" {
		next.ConnectToken = token
	}
	if token := strings.TrimSpace(in.ServiceAccountToken); token != "" {
		next.ServiceAccountToken = token
	}
	switch next.Mode {
	case "off":
		next.Mode = ""
	case "connect":
		u, err := url.Parse(next.ConnectHost)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return OnePassword{}, fmt.Errorf("%w: the Connect host must be an http or https URL", ErrInvalid)
		}
		if next.ConnectToken == "" {
			return OnePassword{}, fmt.Errorf("%w: enter the Connect token", ErrInvalid)
		}
	case "service-account":
		if next.ServiceAccountToken == "" {
			return OnePassword{}, fmt.Errorf("%w: enter the service account token", ErrInvalid)
		}
	default:
		return OnePassword{}, fmt.Errorf("%w: the 1Password mode must be off, connect, or service-account", ErrInvalid)
	}
	if err := s.q.SetOnePassword(ctx, next); err != nil {
		return OnePassword{}, err
	}
	s.agents.SetOnePassword(onePasswordConfig(next.Mode, next.ConnectHost, next.ConnectToken, next.ServiceAccountToken))
	return s.OnePassword(ctx)
}

// ApplyOnePassword gives the saved settings to tools. Call it before agents open.
func (s *Service) ApplyOnePassword(ctx context.Context) error {
	row, err := s.storedOnePassword(ctx)
	if err != nil {
		return err
	}
	s.agents.SetOnePassword(onePasswordConfig(row.Mode, row.ConnectHost, row.ConnectToken, row.ServiceAccountToken))
	return nil
}

func onePasswordConfig(mode, host, connectToken, serviceToken string) engine.OnePassword {
	switch mode {
	case "connect":
		return engine.OnePassword{Mode: mode, ConnectHost: host, ConnectToken: connectToken}
	case "service-account":
		return engine.OnePassword{Mode: mode, ServiceAccountToken: serviceToken}
	}
	return engine.OnePassword{}
}
