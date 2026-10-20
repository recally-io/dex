package host

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

// ProxyServer forwards local web requests to an existing Dex HTTP service.
type ProxyServer struct {
	http      *http.Server
	listener  net.Listener
	done      chan error
	closeOnce sync.Once
	closeErr  error
}

// StartProxy starts a loopback proxy that adds token to requests sent to target.
// The proxy also rewrites Origin so Dex's cross-origin write protection sees the
// request as same-origin with the target service.
func StartProxy(ctx context.Context, target, token string) (*ProxyServer, error) {
	targetURL, err := url.Parse(target)
	if err != nil || targetURL.Scheme == "" || targetURL.Host == "" {
		return nil, fmt.Errorf("invalid proxy target %q", target)
	}
	origin := (&url.URL{Scheme: targetURL.Scheme, Host: targetURL.Host}).String()
	proxy := &httputil.ReverseProxy{}
	proxy.Rewrite = func(req *httputil.ProxyRequest) {
		req.SetURL(targetURL)
		req.Out.Host = targetURL.Host
		req.Out.Header.Set("Origin", origin)
		if token != "" {
			req.Out.Header.Set("Authorization", "Bearer "+token)
		}
	}

	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	server := &ProxyServer{
		http:     &http.Server{Handler: proxy, ReadHeaderTimeout: 10 * time.Second},
		listener: listener,
		done:     make(chan error, 1),
	}
	go func() {
		err := server.http.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		server.done <- err
	}()
	return server, nil
}

// URL returns the local URL served by the proxy.
func (s *ProxyServer) URL() string {
	return (&url.URL{Scheme: "http", Host: s.listener.Addr().String()}).String()
}

// Done reports the proxy server result.
func (s *ProxyServer) Done() <-chan error { return s.done }

// Close stops the proxy.
func (s *ProxyServer) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.closeErr = s.http.Shutdown(ctx)
	})
	return s.closeErr
}
