package feishu

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type connectionConfig struct {
	ReconnectCount    int `json:"ReconnectCount"`
	ReconnectInterval int `json:"ReconnectInterval"`
	ReconnectNonce    int `json:"ReconnectNonce"`
	PingInterval      int `json:"PingInterval"`
}

type fragments struct {
	parts [][]byte
	size  int
	until time.Time
}

// Listen runs the long connection. handle must return only after durable admission.
// report receives safe status errors; it must not block.
func (c *Client) Listen(ctx context.Context, handle func(context.Context, []byte) error, report func(error)) error {
	conf := connectionConfig{ReconnectCount: -1, ReconnectInterval: 120, ReconnectNonce: 30, PingInterval: 120}
	attempts := 0
	connected := false
	for {
		err := c.connection(ctx, handle, &conf, func(err error) {
			if err == nil {
				attempts = 0
				connected = true
			}
			report(err)
		})
		if ctx.Err() != nil {
			return nil //nolint:nilerr // Cancellation is a requested shutdown.
		}
		report(err)
		// ReconnectCount limits the SDK, but dex keeps a configured bot alive.
		attempts++
		delay := time.Duration(max(1, conf.ReconnectInterval)) * time.Second
		if attempts == 1 {
			delay = 0
			if conf.ReconnectNonce > 0 {
				delay = time.Duration(rand.IntN(min(conf.ReconnectNonce, 300)*1000)) * time.Millisecond //nolint:gosec // Reconnect jitter needs no cryptographic randomness.
			}
		}
		if !connected {
			delay = retryDelay(min(attempts-1, 2), "")
		}
		if err := pause(ctx, delay); err != nil {
			return nil //nolint:nilerr // Cancellation is a requested shutdown.
		}
	}
}

func (c *Client) connection(ctx context.Context, handle func(context.Context, []byte) error, conf *connectionConfig, report func(error)) error {
	data, err := c.request(ctx, http.MethodPost, "/callback/ws/endpoint", map[string]string{"AppID": c.cfg.AppID, "AppSecret": c.cfg.AppSecret}, "")
	if err != nil {
		return err
	}
	var endpoint struct {
		Data struct {
			URL    string            `json:"URL"`
			Config *connectionConfig `json:"ClientConfig"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &endpoint); err != nil {
		return err
	}
	if endpoint.Data.Config != nil {
		*conf = *endpoint.Data.Config
	}
	u, err := url.Parse(endpoint.Data.URL)
	if err != nil || u.Host == "" || (u.Scheme != "wss" && (u.Scheme != "ws" || c.cfg.APIURL == "https://open.feishu.cn" || c.cfg.APIURL == "https://open.larksuite.com")) {
		return errors.New("feishu returned an invalid WebSocket endpoint")
	}
	service, err := strconv.ParseUint(u.Query().Get("service_id"), 10, 32)
	if err != nil {
		return errors.New("feishu returned an invalid service ID")
	}
	conn, response, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: c.http})
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		return errors.New("feishu WebSocket connection failed")
	}
	defer func() { _ = conn.CloseNow() }()
	report(nil)
	conn.SetReadLimit(4 << 20)
	session, cancel := context.WithCancel(ctx)
	defer cancel()
	// The ping worker reads configuration under this lock; the reader owns fragment state.
	var mu sync.Mutex
	intervalChanged := make(chan struct{}, 1)
	var wg sync.WaitGroup
	wg.Go(func() {
		timer := time.NewTimer(0)
		defer timer.Stop()
		for {
			select {
			case <-session.Done():
				return
			case <-timer.C:
				ping := frame{service: service, headers: []header{{"type", "ping"}}}
				if err := conn.Write(session, websocket.MessageBinary, ping.marshal()); err != nil {
					cancel()
					return
				}
			case <-intervalChanged:
				// Apply pong timing to the next ping as well as the read deadline.
			}
			mu.Lock()
			interval := max(1, conf.PingInterval)
			mu.Unlock()
			timer.Reset(time.Duration(interval) * time.Second)
		}
	})
	defer func() { cancel(); wg.Wait() }()
	pending := map[string]*fragments{}
	for {
		mu.Lock()
		readTimeout := 2 * time.Duration(max(1, conf.PingInterval)) * time.Second
		mu.Unlock()
		readCtx, readCancel := context.WithTimeout(session, readTimeout)
		kind, b, err := conn.Read(readCtx)
		readCancel()
		if err != nil {
			return errors.New("feishu WebSocket disconnected")
		}
		if kind != websocket.MessageBinary {
			continue
		}
		f, err := parseFrame(b)
		if err != nil {
			return fmt.Errorf("feishu frame: %w", err)
		}
		if f.method == 0 {
			if f.get("type") == "pong" && len(f.payload) > 0 {
				var next connectionConfig
				if err := json.Unmarshal(f.payload, &next); err != nil {
					return err
				}
				mu.Lock()
				changed := max(1, conf.PingInterval) != max(1, next.PingInterval)
				*conf = next
				mu.Unlock()
				if changed {
					select {
					case intervalChanged <- struct{}{}:
					default:
					}
				}
			}
			continue
		}
		if f.method != 1 || f.get("type") != "event" {
			continue
		}
		payload := f.payload
		sumRaw := f.get("sum")
		sum := 1
		if sumRaw != "" {
			sum, err = strconv.Atoi(sumRaw)
			if err != nil {
				return errors.New("invalid fragment count")
			}
		}
		if sum < 1 || sum > 256 {
			return errors.New("invalid fragment count")
		}
		if sum > 1 {
			seq, err := strconv.Atoi(f.get("seq"))
			id := f.get("message_id")
			if err != nil || seq < 0 || seq >= sum || id == "" {
				return errors.New("invalid fragment sequence")
			}
			for key, part := range pending {
				if time.Now().After(part.until) {
					delete(pending, key)
				}
			}
			part := pending[id]
			if part == nil {
				// 64 concurrent messages, 4 MB each; raise only if platform traffic requires it.
				if len(pending) >= 64 {
					return errors.New("too many fragmented messages")
				}
				part = &fragments{parts: make([][]byte, sum), until: time.Now().Add(5 * time.Second)}
				pending[id] = part
			}
			if len(part.parts) != sum {
				return errors.New("fragment count changed")
			}
			part.size += len(payload) - len(part.parts[seq])
			if part.size > 4<<20 {
				return errors.New("fragmented message too large")
			}
			part.parts[seq] = payload
			complete := true
			for _, p := range part.parts {
				if p == nil {
					complete = false
					break
				}
			}
			if !complete {
				continue
			}
			payload = make([]byte, 0, part.size)
			for _, p := range part.parts {
				payload = append(payload, p...)
			}
			delete(pending, id)
		}
		code := 200
		if err := handle(session, payload); err != nil {
			code = 500
		}
		f.payload = []byte(fmt.Sprintf(`{"code":%d}`, code))
		if err := conn.Write(session, websocket.MessageBinary, f.marshal()); err != nil {
			return err
		}
	}
}
