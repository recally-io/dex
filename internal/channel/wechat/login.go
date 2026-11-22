package wechat

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// LoginView contains QR setup state, without credentials or the protocol polling key.
type LoginView struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	QRContent string `json:"qrContent"`
	Active    bool   `json:"active"`
	Reason    string `json:"reason,omitempty"`
}

// Login is one temporary QR session. The settings service owns the current session.
type Login struct {
	mu        sync.Mutex
	client    *client
	view      LoginView
	code      string
	pollURL   string
	attempts  int
	candidate *Config
	failure   error
}

// BeginLogin requests the first QR code. At most three replacements are used in one session.
func BeginLogin(ctx context.Context, id string, options Options) (*Login, LoginView, error) {
	c := newClient(options)
	l := &Login{client: c, pollURL: c.options.APIURL, view: LoginView{ID: id, Status: "wait", Active: true}}
	err := l.refresh(ctx)
	return l, l.view, err
}

func (l *Login) refresh(ctx context.Context) error {
	if l.attempts >= 4 {
		l.view.Active = false
		l.view.Reason = "All three QR refreshes were used. Start again."
		return nil
	}
	var qr struct {
		Code    string `json:"qrcode"`
		Content string `json:"qrcode_img_content"`
	}
	err := l.client.call(ctx, l.client.options.APIURL, "", http.MethodPost, "get_bot_qrcode?bot_type=3", map[string]any{"local_token_list": []string{}}, &qr, 35*time.Second)
	if err != nil {
		return err
	}
	if qr.Code == "" || qr.Content == "" {
		return errors.New("WeChat returned an empty QR code")
	}
	l.code = qr.Code
	l.view.QRContent = qr.Content
	l.pollURL = l.client.options.APIURL
	l.attempts++
	return nil
}

// Poll waits for one protocol state. Activation publishes local confirmation.
func (l *Login) Poll(ctx context.Context, verifyCode string) (LoginView, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.view.Active || l.candidate != nil {
		return l.view, l.failure
	}
	endpoint := "get_qrcode_status?qrcode=" + url.QueryEscape(l.code)
	if verifyCode != "" {
		endpoint += "&verify_code=" + url.QueryEscape(verifyCode)
	}
	var state struct {
		Status       string `json:"status"`
		Token        string `json:"bot_token"`
		BotID        string `json:"ilink_bot_id"`
		Owner        string `json:"ilink_user_id"`
		BaseURL      string `json:"baseurl"`
		RedirectHost string `json:"redirect_host"`
	}
	err := l.client.call(ctx, l.pollURL, "", http.MethodGet, endpoint, nil, &state, 35*time.Second)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		l.view.Status = "wait"
		return l.view, nil
	}
	if err != nil {
		return l.view, err
	}
	l.view.Status = state.Status
	l.view.Reason = ""
	switch state.Status {
	case "wait", "scaned", "need_verifycode":
	case "scaned_but_redirect":
		target := "https://" + state.RedirectHost
		parsed, parseErr := url.Parse(target)
		if parseErr != nil || state.RedirectHost == "" || parsed.Host != state.RedirectHost || parsed.Path != "" || strings.ContainsAny(state.RedirectHost, "/?#@") {
			return l.view, errors.New("invalid WeChat QR redirect host")
		}
		l.pollURL = target
	case "expired", "verify_code_blocked":
		if err = l.refresh(ctx); err != nil {
			return l.view, err
		}
	case "binded_redirect":
		l.view.Active = false
		l.view.Reason = "This bot is already linked. No new credentials were returned."
	case "confirmed":
		cfg := &Config{Token: state.Token, BotID: state.BotID, Owner: state.Owner, BaseURL: state.BaseURL}
		if err = cfg.Validate(); err != nil {
			l.view.Status = "scaned"
			l.view.Active = false
			l.failure = err
			return l.view, err
		}
		l.view.Status = "scaned"
		l.candidate = cfg
		return l.view, nil
	default:
		return l.view, errors.New("unknown WeChat QR status")
	}
	return l.view, nil
}

// Activate retains candidate credentials until settings and runtime activation succeed.
func (l *Login) Activate(save func(*Config) error) (LoginView, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.candidate == nil {
		return l.view, l.failure
	}
	if err := save(l.candidate); err != nil {
		return l.view, err
	}
	l.candidate = nil
	l.view.Status = "confirmed"
	l.view.Active = false
	return l.view, nil
}
