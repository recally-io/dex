package worker

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestUserRPCCancellationWithFullPipe(t *testing.T) {
	// Given a real pipe whose reader is open but does not consume requests.
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	p := &process{stdin: writer, writer: make(chan struct{}, 1), pending: map[int64]chan incoming{}, done: make(chan struct{})}
	// When one request fills the pipe and another waits for the writer, both deadlines apply.
	results := make(chan error, 2)
	for range 2 {
		go func() {
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			results <- p.call(ctx, "ping", strings.Repeat("x", 8<<20), &struct{}{})
		}()
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	deadlines := 0
	for range 2 {
		select {
		case err := <-results:
			if errors.Is(err, context.DeadlineExceeded) {
				deadlines++
			}
			if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, ErrUnavailable) {
				t.Fatalf("RPC did not preserve its deadline: %v", err)
			}
		case <-deadline.C:
			t.Fatal("RPC ignored cancellation while writing or waiting for the pipe")
		}
	}
	if deadlines == 0 {
		t.Fatal("full pipe did not honor a write deadline")
	}
}

func TestUserRPCWriterWaitHonorsCancellation(t *testing.T) {
	// Given a writer reserved by another call, without a worker consuming stdin.
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	p := &process{stdin: writer, writer: make(chan struct{}, 1), pending: map[int64]chan incoming{}, done: make(chan struct{})}
	p.writer <- struct{}{}
	// When the waiting caller's deadline expires, it must return without the writer.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := p.call(ctx, "ping", struct{}{}, &struct{}{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("writer wait ignored deadline: %v", err)
	}
}
