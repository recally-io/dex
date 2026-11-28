package settings

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// CheckToken accepts 32 to 256 base64url characters. The length is a floor, not proof of entropy:
// a user token must be random, because the API does not limit sign-in attempts.
func CheckToken(token string) error {
	if len(token) < 32 || len(token) > 256 || strings.Trim(token, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
		return errors.New("the token must be 32 to 256 characters from A-Z, a-z, 0-9, '-' and '_'; generate one with: openssl rand -base64 32 | tr '+/' '-_' | tr -d '='")
	}
	return nil
}

// AccessToken returns the stored access token. The first call creates it; later calls never
// replace it.
func (s *Service) AccessToken(ctx context.Context) (string, error) {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never fails; it crashes the program instead
	err := s.q.CreateAccessTokenIfMissing(ctx, base64.RawURLEncoding.EncodeToString(b))
	if err != nil {
		return "", fmt.Errorf("create the access token: %w", err)
	}
	token, err := s.q.GetAccessToken(ctx)
	if err != nil {
		return "", fmt.Errorf("read the access token: %w", err)
	}
	return token, nil
}
