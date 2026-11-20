package runner

import (
	"context"
	"time"

	"github.com/coder/websocket"
)

// The socket reader handles pong frames while the heartbeat waits for a reply.
func heartbeat(ctx context.Context, socket *websocket.Conn, disconnect func()) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := socket.Ping(deadline)
			cancel()
			if err != nil && ctx.Err() == nil {
				disconnect()
				return
			}
		}
	}
}
