package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// The Session tests inject channel routes without importing platform adapters.
// A test that supplies an account uses the actual SQLite binding.
func bindTestChannelPolicy(s *Service) {
	s.ConfigureChannelPolicy(ChannelPolicy{
		CheckPeer: func(peer string) error {
			if !testChannelRoute(peer).Owner {
				return ErrChannelUnauthorized
			}
			return nil
		},
		CheckInputIn: func(ctx context.Context, q *sqlc.Queries, botID, peer string, source ChannelContext) error {
			if source.AccountID == "" {
				return nil
			}
			paired, err := q.ChannelPaired(ctx, sqlc.ChannelPairedParams{BotID: botID, Peer: peer, AccountID: source.AccountID})
			if err == nil && !paired {
				return ErrChannelUnauthorized
			}
			return err
		},
		CheckCommandIn: func(context.Context, *sqlc.Queries, string, string, int64) error { return nil },
		Route:          testChannelRoute,
	})
}

func testChannelRoute(peer string) ChannelRoute {
	platform, _, _ := strings.Cut(peer, ":")
	switch platform {
	case "telegram":
		return ChannelRoute{User: true, Owner: true, Quote: true, Files: true}
	case "feishu":
		return ChannelRoute{User: true, Owner: true, Quote: true}
	case "wechat":
		return ChannelRoute{User: true, Owner: true}
	default:
		return ChannelRoute{}
	}
}

func TestUserWithoutChannelPolicyCanOnlyActFromWeb(t *testing.T) {
	t.Parallel()
	r := startNative(t, "echo")
	s := New(r.conn, r.bots, time.Now, nil, nil)
	t.Cleanup(func() { s.cancel(); s.wg.Wait() })
	ctx := t.Context()
	const peer, source = "test:peer", "test:command"

	if err := s.AcceptMessage(ctx, "bot", peer, source, Message{Content: "hello"}); !errors.Is(err, ErrChannelUnauthorized) {
		t.Fatalf("channel intake without policy: %v", err)
	}
	if main, err := s.ExistingMain(ctx, "bot"); err != nil || main != nil {
		t.Fatalf("rejected input created Main: %+v, %v", main, err)
	}
	target, err := s.Main(ctx, "bot")
	if err != nil {
		t.Fatal(err)
	}
	for name, act := range map[string]func() error{
		"stop":  func() error { return s.StopChannelCommand(ctx, "bot", target.ID, source, "stop") },
		"clear": func() error { return s.Clear(ctx, "bot", target.ID, "channel:"+source, peer, "") },
		"resume": func() error {
			return s.ResumeChildFromSource(ctx, "bot", target, peer, source, source, "1", "", ChannelContext{})
		},
	} {
		if err := act(); !errors.Is(err, ErrChannelUnauthorized) {
			t.Fatalf("channel %s without policy: %v", name, err)
		}
	}
	if err := s.Clear(ctx, "bot", target.ID, "web-clear", "web", ""); err != nil {
		t.Fatalf("web Clear needs no channel policy: %v", err)
	}
}
