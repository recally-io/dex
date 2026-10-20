// Package host starts Dex's HTTP server around the control application.
package host

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/recally-io/dex/internal/app"
)

// Config configures a running HTTP host.
type Config struct {
	Addr string
	App  app.Config
}

// Server is a running Dex HTTP host.
type Server struct {
	app       *app.App
	http      *http.Server
	listener  net.Listener
	done      chan error
	closeOnce sync.Once
	closeErr  error
}

// Start listens on cfg.Addr, starts the Dex application, and serves its handler.
func Start(ctx context.Context, cfg Config) (*Server, error) {
	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		return nil, err
	}
	running, err := app.Start(ctx, cfg.App)
	if err != nil {
		return nil, errors.Join(err, listener.Close())
	}
	server := &Server{
		app:      running,
		http:     &http.Server{Addr: listener.Addr().String(), Handler: running.Handler(), ReadHeaderTimeout: 10 * time.Second},
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

// URL returns the loopback or configured URL that serves the web UI and API.
func (s *Server) URL() string {
	return (&url.URL{Scheme: "http", Host: s.listener.Addr().String()}).String()
}

// Done reports the HTTP server result. A nil value means a deliberate shutdown.
func (s *Server) Done() <-chan error { return s.done }

// Close stops HTTP requests and the Dex application.
func (s *Server) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		httpErr := s.http.Shutdown(ctx)
		appErr := s.app.Close() //nolint:contextcheck // App.Close owns its cleanup context.
		s.closeErr = errors.Join(httpErr, appErr)
	})
	return s.closeErr
}
