package appserver

import (
	"context"
	"encoding/json"
)

const (
	clientName    = "codex-remote-bridge"
	clientVersion = "0.1.0"
)

// Initialize performs the handshake and sends the `initialized` notification.
// Pass experimental=true to unlock experimentalApi-gated methods (e.g. thread/turns/list).
func (c *Client) Initialize(ctx context.Context, experimental bool) (json.RawMessage, error) {
	params := InitializeParams{
		ClientInfo:   ClientInfo{Name: clientName, Version: clientVersion},
		Capabilities: &InitializeCapabilities{ExperimentalAPI: experimental},
	}
	res, err := c.Call(ctx, "initialize", params)
	if err != nil {
		return nil, err
	}
	if err := c.Notify("initialized", map[string]any{}); err != nil {
		return nil, err
	}
	return res, nil
}

// ThreadList returns recent threads (the user's existing sessions).
func (c *Client) ThreadList(ctx context.Context, limit int) (*ThreadListResult, error) {
	res, err := c.Call(ctx, "thread/list", ThreadListParams{Limit: limit})
	if err != nil {
		return nil, err
	}
	var out ThreadListResult
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ThreadListArchivedIDs returns the set of archived thread ids (best-effort),
// used to subtract archived threads from the active list on older codex builds
// whose default list still includes them.
func (c *Client) ThreadListArchivedIDs(ctx context.Context, limit int) (map[string]struct{}, error) {
	res, err := c.Call(ctx, "thread/list", ThreadListParams{Limit: limit, Archived: true})
	if err != nil {
		return nil, err
	}
	var out ThreadListResult
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, err
	}
	ids := make(map[string]struct{}, len(out.Data))
	for _, t := range out.Data {
		ids[t.ID] = struct{}{}
	}
	return ids, nil
}

// ThreadRead returns a thread's full content (raw, caller decodes as needed).
func (c *Client) ThreadRead(ctx context.Context, threadID string, includeTurns bool) (json.RawMessage, error) {
	return c.Call(ctx, "thread/read", ThreadReadParams{ThreadID: threadID, IncludeTurns: includeTurns})
}

// ThreadStart creates a new thread and returns its id.
func (c *Client) ThreadStart(ctx context.Context, p ThreadStartParams) (string, error) {
	res, err := c.Call(ctx, "thread/start", p)
	if err != nil {
		return "", err
	}
	var out ThreadStartResult
	if err := json.Unmarshal(res, &out); err != nil {
		return "", err
	}
	return out.Thread.ID, nil
}

// TurnStart begins a turn (sends a prompt). For remote-initiated turns the
// bridge should always pass a safe ApprovalPolicy + SandboxPolicy override,
// because the local config defaults to danger-full-access / approval=never.
func (c *Client) TurnStart(ctx context.Context, p TurnStartParams) (json.RawMessage, error) {
	return c.Call(ctx, "turn/start", p)
}
