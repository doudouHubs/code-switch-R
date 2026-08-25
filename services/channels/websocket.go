package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type wsClient struct {
	mu        sync.RWMutex
	writeMu   sync.Mutex
	conn      *websocket.Conn
	cancel    context.CancelFunc
	done      chan struct{}
	running   bool
	onMessage func([]byte)
	onError   func(error)
}

func newWSClient(ctx context.Context, endpoint string, onMessage func([]byte), onError func(error)) (*wsClient, error) {
	if endpoint == "" {
		return nil, errors.New("channel websocket endpoint is empty")
	}
	connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, _, err := websocket.DefaultDialer.DialContext(connectCtx, endpoint, nil)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("connect channel websocket: %w", err)
	}
	clientCtx, clientCancel := context.WithCancel(ctx)
	client := &wsClient{conn: conn, cancel: clientCancel, done: make(chan struct{}), running: true, onMessage: onMessage, onError: onError}
	go client.readLoop(clientCtx)
	return client, nil
}

func (c *wsClient) readLoop(ctx context.Context) {
	defer close(c.done)
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			c.running = false
			c.mu.Unlock()
			if ctx.Err() == nil && c.onError != nil {
				c.onError(err)
			}
			return
		}
		if c.onMessage != nil {
			c.onMessage(data)
		}
	}
}

func (c *wsClient) WriteJSON(value any) error {
	if c == nil {
		return errors.New("channel websocket is unavailable")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.RLock()
	conn := c.conn
	running := c.running
	c.mu.RUnlock()
	if !running || conn == nil {
		return errors.New("channel websocket is not connected")
	}
	return conn.WriteJSON(value)
}

func (c *wsClient) WriteMessage(data []byte) error {
	if c == nil {
		return errors.New("channel websocket is unavailable")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.RLock()
	conn := c.conn
	running := c.running
	c.mu.RUnlock()
	if !running || conn == nil {
		return errors.New("channel websocket is not connected")
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}

func (c *wsClient) Close() error {
	if c == nil {
		return nil
	}
	c.cancel()
	c.mu.Lock()
	conn := c.conn
	c.running = false
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
	}
	return nil
}

func decodeJSONObject(data []byte) map[string]any {
	var value map[string]any
	if json.Unmarshal(data, &value) != nil {
		return nil
	}
	return value
}
