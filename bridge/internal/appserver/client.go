package appserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
)

// NotificationHandler receives server->client notifications (no id).
type NotificationHandler func(method string, params json.RawMessage)

// ServerRequestHandler receives server->client requests (with id) such as
// command/patch approval prompts. Return either a result or an error.
type ServerRequestHandler func(method string, params json.RawMessage) (result any, err error)

// Client drives a spawned `codex app-server` child over its stdio.
type Client struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser

	writeMu sync.Mutex
	nextID  int64

	mu      sync.Mutex
	pending map[int64]chan *rpcMessage

	OnNotification  NotificationHandler
	OnServerRequest ServerRequestHandler
	OnStderr        func(line string)
}

// New spawns `codexBin app-server [args...]` and starts the read loop.
func New(codexBin string, args ...string) (*Client, error) {
	cmd := exec.Command(codexBin, append([]string{"app-server"}, args...)...)
	hideSpawnWindow(cmd) // Windows: no console window for the child (see spawn_windows.go)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	c := &Client{cmd: cmd, stdin: stdin, pending: make(map[int64]chan *rpcMessage)}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}
	go c.readLoop(stdout)
	go c.readStderr(stderr)
	return c, nil
}

func (c *Client) readStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if c.OnStderr != nil {
			c.OnStderr(sc.Text())
		} else {
			// OnStderr may be wired after New() returns; don't drop the child's
			// early startup/death output (it's how we diagnose a failed spawn).
			fmt.Fprintln(os.Stderr, "[codex] "+sc.Text())
		}
	}
}

func (c *Client) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	// app-server frames can be large (thread/read with full history); raise caps.
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var m rpcMessage
		if err := json.Unmarshal(line, &m); err != nil {
			continue // tolerate non-JSON noise on stdout
		}
		switch {
		case m.Method != "" && len(m.ID) > 0:
			go c.handleServerRequest(&m) // approval prompt etc.
		case m.Method != "":
			if c.OnNotification != nil {
				c.OnNotification(m.Method, m.Params)
			}
		case len(m.ID) > 0:
			id, err := strconv.ParseInt(string(m.ID), 10, 64)
			if err != nil {
				continue
			}
			c.mu.Lock()
			ch := c.pending[id]
			c.mu.Unlock()
			if ch != nil {
				ch <- &m
			}
		}
	}
}

func (c *Client) handleServerRequest(m *rpcMessage) {
	var result any
	var err error
	if c.OnServerRequest != nil {
		result, err = c.OnServerRequest(m.Method, m.Params)
	} else {
		err = fmt.Errorf("no server-request handler")
	}
	out := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(m.ID)}
	if err != nil {
		out["error"] = &rpcError{Code: -32000, Message: err.Error()}
	} else {
		out["result"] = result
	}
	_ = c.writeRaw(out)
}

func (c *Client) writeRaw(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(b)
	return err
}

// Call sends a request and blocks until the matching response (or ctx done).
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := atomic.AddInt64(&c.nextID, 1)
	ch := make(chan *rpcMessage, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	var praw json.RawMessage = json.RawMessage("{}")
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		praw = b
	}
	if err := c.writeRaw(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": praw}); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case m := <-ch:
		if m.Error != nil {
			return nil, fmt.Errorf("rpc %s error %d: %s", method, m.Error.Code, m.Error.Message)
		}
		return m.Result, nil
	}
}

// Notify sends a notification (no id, no response).
func (c *Client) Notify(method string, params any) error {
	var praw json.RawMessage = json.RawMessage("{}")
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		praw = b
	}
	return c.writeRaw(map[string]any{"jsonrpc": "2.0", "method": method, "params": praw})
}

// Close shuts the child down.
func (c *Client) Close() error {
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	return c.cmd.Wait()
}
