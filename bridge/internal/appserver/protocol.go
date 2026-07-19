// Package appserver speaks the Codex `app-server` JSON-RPC 2.0 protocol
// (newline-delimited JSON over a spawned `codex app-server` child's stdio).
//
// Only the subset of types the bridge needs is modeled here. The authoritative
// shapes live in ../../protocol/ (exported via `codex app-server
// generate-json-schema`). Keep this in sync with the pinned Codex version.
package appserver

import "encoding/json"

// rpcError is a JSON-RPC 2.0 error object.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// rpcMessage is a wire frame in either direction (request/response/notification).
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// ---- initialize ----

type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Title   string `json:"title,omitempty"`
}

type InitializeCapabilities struct {
	ExperimentalAPI bool `json:"experimentalApi,omitempty"`
}

type InitializeParams struct {
	ClientInfo   ClientInfo              `json:"clientInfo"`
	Capabilities *InitializeCapabilities `json:"capabilities,omitempty"`
}

// ---- thread/list ----

type ThreadListParams struct {
	Limit      int    `json:"limit,omitempty"`
	Cursor     string `json:"cursor,omitempty"`
	SearchTerm string `json:"searchTerm,omitempty"`
	// Archived filters the listing: false (the default we always send) returns
	// only non-archived threads, so a thread archived on the desktop never shows
	// up on the phone. Sent explicitly rather than relying on the server default.
	Archived bool `json:"archived"`
}

type ThreadSummary struct {
	ID        string `json:"id"`      // thread/session id
	Name      string `json:"name"`    // AI-generated short title (may be empty/null)
	Preview   string `json:"preview"` // first user message
	Cwd       string `json:"cwd"`
	UpdatedAt int64  `json:"updatedAt"`
	Source    string `json:"source"` // e.g. "vscode", "cli"
	Status    struct {
		Type string `json:"type"` // e.g. "notLoaded", "loaded"
	} `json:"status"`
}

// Title returns the best human label for a session.
func (t ThreadSummary) Title() string {
	if t.Name != "" {
		return t.Name
	}
	return t.Preview
}

type ThreadListResult struct {
	Data       []ThreadSummary `json:"data"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

// ---- thread/read ----

type ThreadReadParams struct {
	ThreadID     string `json:"threadId"`
	IncludeTurns bool   `json:"includeTurns,omitempty"`
}

// ---- thread/start ----  (sandbox is a SandboxMode string here)

type ThreadStartParams struct {
	Cwd            string `json:"cwd,omitempty"`
	Sandbox        string `json:"sandbox,omitempty"`        // read-only | workspace-write | danger-full-access
	ApprovalPolicy string `json:"approvalPolicy,omitempty"` // untrusted | on-failure | on-request | never
	Ephemeral      bool   `json:"ephemeral,omitempty"`
}

type ThreadStartResult struct {
	Thread struct {
		ID string `json:"id"`
	} `json:"thread"`
}

// ---- turn/start ----  (sandboxPolicy is an OBJECT here, distinct from thread/start)

type TextInput struct {
	Type string `json:"type"` // always "text"
	Text string `json:"text"`
}

func Text(s string) TextInput { return TextInput{Type: "text", Text: s} }

// ImageInput is an image turn-input item (Codex `ImageUserInput`). URL may be a
// remote URL or a data: URL (e.g. data:image/jpeg;base64,...); Codex accepts both.
type ImageInput struct {
	Type string `json:"type"` // always "image"
	URL  string `json:"url"`
}

func Image(url string) ImageInput { return ImageInput{Type: "image", URL: url} }

type TurnStartParams struct {
	ThreadID string `json:"threadId"`
	// Input is a heterogeneous list of turn-input items (TextInput / ImageInput).
	Input             []any           `json:"input"`
	ApprovalPolicy    string          `json:"approvalPolicy,omitempty"`
	ApprovalsReviewer string          `json:"approvalsReviewer,omitempty"` // user | auto_review
	SandboxPolicy     json.RawMessage `json:"sandboxPolicy,omitempty"`
	Cwd               string          `json:"cwd,omitempty"`
	Model             string          `json:"model,omitempty"`
	Effort            string          `json:"effort,omitempty"`      // ReasoningEffort: none|minimal|low|medium|high|xhigh
	Summary           string          `json:"summary,omitempty"`     // ReasoningSummary: auto|concise|detailed|none
	ServiceTier       string          `json:"serviceTier,omitempty"` // fast | flex
}

// SandboxReadOnly is the strictest sandbox policy (no writes, no network).
var SandboxReadOnly = json.RawMessage(`{"type":"readOnly","networkAccess":false}`)

// SandboxWorkspaceWrite allows writes within the workspace but keeps network
// access restricted — the ceiling for remote-initiated turns.
var SandboxWorkspaceWrite = json.RawMessage(`{"type":"workspaceWrite","networkAccess":false}`)

// DeltaNotification matches agentMessage/delta and commandExecution/outputDelta.
type DeltaNotification struct {
	Delta    string `json:"delta"`
	ItemID   string `json:"itemId"`
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}
