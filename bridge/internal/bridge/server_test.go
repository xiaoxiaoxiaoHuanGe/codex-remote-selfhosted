package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type capturePusher struct{ ch chan Notification }

func (p *capturePusher) Push(_ []PushReg, n Notification) { p.ch <- n }

// pushNotify must alert registered devices when no phone is connected, and stay
// silent when a phone is in the foreground (it already sees the live WS event).
func TestPushNotifyGatedByClients(t *testing.T) {
	cp := &capturePusher{ch: make(chan Notification, 4)}
	s := &Server{
		clients: make(map[*conn]struct{}),
		pushes:  map[string]PushReg{"dev1": {Token: "dev1", Platform: "ios"}},
		pusher:  cp,
	}

	s.pushNotify(Notification{Kind: "approval"})
	select {
	case n := <-cp.ch:
		if n.Kind != "approval" {
			t.Fatalf("got kind %q", n.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("expected a push when no client is connected")
	}

	// With a client connected, the push must be suppressed.
	s.clients[&conn{send: make(chan any, 1)}] = struct{}{}
	s.pushNotify(Notification{Kind: "turnDone"})
	select {
	case <-cp.ch:
		t.Fatal("push must be suppressed while a phone is connected")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestRegisterPush(t *testing.T) {
	s := &Server{
		clients: make(map[*conn]struct{}),
		pushes:  make(map[string]PushReg),
		pusher:  LogPusher{},
	}
	c := &conn{send: make(chan any, 4)}
	s.handle(context.Background(), c, inbound{Type: "registerPush", PushToken: "dev9", Platform: "ios"})

	if _, ok := s.pushes["dev9"]; !ok {
		t.Fatal("device token not registered")
	}
	select {
	case m := <-c.send:
		if mm, ok := m.(map[string]any); !ok || mm["type"] != "pushRegistered" {
			t.Fatalf("expected pushRegistered ack, got %v", m)
		}
	default:
		t.Fatal("no ack sent to client")
	}
}

// Pending approvals must be snapshot-able so they can be re-surfaced to a phone
// that reconnects after backgrounding (the resync path).
func TestPendingApprovalsSnapshot(t *testing.T) {
	s := &Server{approvals: make(map[string]*pendingApproval)}
	s.approvals["a"] = &pendingApproval{id: "a", method: "execCommandApproval"}
	s.approvals["b"] = &pendingApproval{id: "b", method: "item/commandExecution/requestApproval"}

	got := s.pendingApprovals()
	if len(got) != 2 {
		t.Fatalf("got %d pending, want 2", len(got))
	}
	seen := map[string]bool{}
	for _, p := range got {
		seen[p.id] = true
	}
	if !seen["a"] || !seen["b"] {
		t.Fatalf("missing approvals in snapshot: %v", seen)
	}
}

// clampDecision must map the phone's coarse accept/decline to the exact
// protocol-valid value, and must NEVER forward the persistent/amendment variants
// that would disable the per-command approval gate for the rest of the session.
func TestClampDecision(t *testing.T) {
	cases := []struct{ method, in, want string }{
		// ReviewDecision family (exec command / apply patch).
		{"execCommandApproval", "accept", "approved"},
		{"execCommandApproval", "decline", "denied"},
		{"applyPatchApproval", "accept", "approved"},
		{"applyPatchApproval", "decline", "denied"},
		// CommandExecution / fileChange family.
		{"item/commandExecution/requestApproval", "accept", "accept"},
		{"item/commandExecution/requestApproval", "decline", "decline"},
		{"item/fileChange/requestApproval", "accept", "accept"},
		// Persistent / amendment / unknown variants must collapse to a refusal.
		{"item/commandExecution/requestApproval", "acceptForSession", "decline"},
		{"execCommandApproval", "approved_for_session", "denied"},
		{"item/commandExecution/requestApproval", "acceptWithExecpolicyAmendment", "decline"},
		{"execCommandApproval", "approved_execpolicy_amendment", "denied"},
		{"execCommandApproval", "", "denied"},
		{"item/commandExecution/requestApproval", "", "decline"},
	}
	for _, c := range cases {
		if got := clampDecision(c.method, c.in); got != c.want {
			t.Errorf("clampDecision(%q, %q) = %q, want %q", c.method, c.in, got, c.want)
		}
	}
}

// handleFile must serve only media files inside an allowed root, and must reject
// credentials/config/source even when they sit inside that root.
func TestHandleFileMediaGate(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "pic.png"), "\x89PNG\r\n\x1a\n....")
	write(t, filepath.Join(root, "clip.mp4"), "\x00\x00\x00\x18ftypmp42")
	write(t, filepath.Join(root, "auth.json"), `{"access_token":"secret"}`)
	write(t, filepath.Join(root, "config.toml"), "danger=true")
	// A secret copied to a media NAME but with non-media CONTENT must be rejected
	// by the magic-byte sniff (filename-only allowlist is not enough — finding #7).
	write(t, filepath.Join(root, "stolen.png"), "-----BEGIN OPENSSH PRIVATE KEY-----\n")
	write(t, filepath.Join(outside, "evil.png"), "\x89PNG\r\n\x1a\n")

	s := &Server{tokens: map[string]string{"good": "default"}, fileRoots: []string{root}}

	get := func(path, token string) int {
		q := url.Values{"path": {path}, "token": {token}}
		r := httptest.NewRequest(http.MethodGet, "/file?"+q.Encode(), nil)
		w := httptest.NewRecorder()
		s.handleFile(w, r)
		return w.Code
	}

	cases := []struct {
		name string
		path string
		tok  string
		want int
	}{
		{"png served", filepath.Join(root, "pic.png"), "good", http.StatusOK},
		{"mp4 served", filepath.Join(root, "clip.mp4"), "good", http.StatusOK},
		{"auth.json rejected", filepath.Join(root, "auth.json"), "good", http.StatusForbidden},
		{"config.toml rejected", filepath.Join(root, "config.toml"), "good", http.StatusForbidden},
		{"renamed secret rejected", filepath.Join(root, "stolen.png"), "good", http.StatusForbidden},
		{"bad token", filepath.Join(root, "pic.png"), "wrong", http.StatusUnauthorized},
		{"empty token", filepath.Join(root, "pic.png"), "", http.StatusUnauthorized},
		{"png outside root", filepath.Join(outside, "evil.png"), "good", http.StatusForbidden},
		{"missing png", filepath.Join(root, "nope.png"), "good", http.StatusNotFound},
	}
	for _, c := range cases {
		if got := get(c.path, c.tok); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

// mediaRoots mirrors workspaceRoots, so a session's working tree is always
// viewable and a SINGLE knob (CODEX_WORKSPACE_ROOTS) governs both what the phone
// can operate on and what images it can see. Narrowing the workspace narrows the
// media allowlist with it.
func TestMediaRootsFollowsWorkspace(t *testing.T) {
	t.Setenv("CODEX_MEDIA_ROOTS", "")
	has := func(roots []string, p string) bool {
		for _, r := range roots {
			if r == p {
				return true
			}
		}
		return false
	}
	a, _ := filepath.EvalSymlinks(t.TempDir())
	b, _ := filepath.EvalSymlinks(t.TempDir())

	t.Setenv("CODEX_WORKSPACE_ROOTS", a)
	if got := mediaRoots(); !has(got, a) {
		t.Fatalf("mediaRoots should follow CODEX_WORKSPACE_ROOTS=%q; got %v", a, got)
	}
	// Narrowing the workspace narrows media too: the old root must drop out.
	t.Setenv("CODEX_WORKSPACE_ROOTS", b)
	got := mediaRoots()
	if !has(got, b) {
		t.Fatalf("mediaRoots should follow CODEX_WORKSPACE_ROOTS=%q; got %v", b, got)
	}
	if has(got, a) {
		t.Fatalf("narrowed mediaRoots must drop old root %q; got %v", a, got)
	}
}

// An empty configured token set must reject everything (fail closed).
func TestTokenOKEmptyConfig(t *testing.T) {
	s := &Server{tokens: nil}
	r := httptest.NewRequest(http.MethodGet, "/ws?token=", nil)
	if s.tokenOK(r) {
		t.Fatal("empty configured token set must reject all requests")
	}
}

// Multiple named device tokens each authenticate and return their device name.
func TestAuthDeviceMultiToken(t *testing.T) {
	s := &Server{tokens: map[string]string{"tokA": "phone", "tokB": "ipad"}}
	req := func(tok string) *http.Request {
		return httptest.NewRequest(http.MethodGet, "/ws?token="+tok, nil)
	}
	if d, ok := s.authDevice(req("tokA")); !ok || d != "phone" {
		t.Fatalf("tokA -> %q,%v want phone,true", d, ok)
	}
	if d, ok := s.authDevice(req("tokB")); !ok || d != "ipad" {
		t.Fatalf("tokB -> %q,%v want ipad,true", d, ok)
	}
	if _, ok := s.authDevice(req("nope")); ok {
		t.Fatal("wrong token must be rejected")
	}
	empty := &Server{tokens: nil}
	if _, ok := empty.authDevice(req("tokA")); ok {
		t.Fatal("no configured tokens must reject everything")
	}
}

// An approval may be answered ONLY by the device whose turn triggered it; an
// unowned approval may be answered by anyone.
func TestApprovalOwnershipBinding(t *testing.T) {
	s := &Server{
		clients:   make(map[*conn]struct{}),
		approvals: make(map[string]*pendingApproval),
	}
	owned := &pendingApproval{id: "ap1", method: "execCommandApproval", ownerDevice: "phone", ch: make(chan string, 1)}
	unowned := &pendingApproval{id: "ap2", method: "execCommandApproval", ownerDevice: "", ch: make(chan string, 1)}
	s.approvals["ap1"] = owned
	s.approvals["ap2"] = unowned

	ipad := &conn{send: make(chan any, 4), id: "i1", device: "ipad"}
	phone := &conn{send: make(chan any, 4), id: "p1", device: "phone"}

	// Foreign device cannot answer an owned approval.
	s.handle(context.Background(), ipad, inbound{Type: "approvalDecision", ID: "ap1", Decision: "accept"})
	select {
	case <-owned.ch:
		t.Fatal("foreign device must not answer an owned approval")
	default:
	}
	if m, ok := (<-ipad.send).(map[string]any); !ok || m["type"] != "error" {
		t.Fatal("foreign device should get a rejection error")
	}

	// Owner device can answer it.
	s.handle(context.Background(), phone, inbound{Type: "approvalDecision", ID: "ap1", Decision: "accept"})
	select {
	case dec := <-owned.ch:
		if dec != "accept" {
			t.Fatalf("owner decision = %q", dec)
		}
	default:
		t.Fatal("owner device decision not delivered")
	}

	// Unowned approval is answerable when unambiguous (no/one connected device).
	s.handle(context.Background(), ipad, inbound{Type: "approvalDecision", ID: "ap2", Decision: "decline"})
	select {
	case dec := <-unowned.ch:
		if dec != "decline" {
			t.Fatalf("unowned decision = %q", dec)
		}
	default:
		t.Fatal("unowned approval should accept a decision when unambiguous")
	}
}

// An unowned approval must NOT be answerable when more than one distinct device
// is connected (ambiguous — could belong to either). Fail closed.
func TestUnownedApprovalAmbiguous(t *testing.T) {
	s := &Server{clients: make(map[*conn]struct{}), approvals: make(map[string]*pendingApproval)}
	unowned := &pendingApproval{id: "x", method: "execCommandApproval", ownerDevice: "", ch: make(chan string, 1)}
	s.approvals["x"] = unowned
	phone := &conn{send: make(chan any, 4), id: "p", device: "phone"}
	ipad := &conn{send: make(chan any, 4), id: "i", device: "ipad"}
	s.clients[phone] = struct{}{}
	s.clients[ipad] = struct{}{}

	s.handle(context.Background(), ipad, inbound{Type: "approvalDecision", ID: "x", Decision: "accept"})
	select {
	case <-unowned.ch:
		t.Fatal("ambiguous unowned approval must not be answerable")
	default:
	}
	if m, ok := (<-ipad.send).(map[string]any); !ok || m["type"] != "error" {
		t.Fatal("expected ambiguous-rejection error")
	}
}

// clearActiveTurn must drop both the active turn and the turn owner (bounds map
// growth + prevents stale ownership on thread reuse).
func TestClearActiveTurnClearsOwner(t *testing.T) {
	s := &Server{turns: make(map[string]string), turnOwner: make(map[string]string)}
	s.setTurnOwner("t1", "phone")
	s.setActiveTurn("t1", "turn1")
	s.clearActiveTurn("t1")
	if s.ownerOf("t1") != "" {
		t.Fatal("turnOwner should be cleared")
	}
	if s.activeTurn("t1") != "" {
		t.Fatal("active turn should be cleared")
	}
}

// A token reused across devices (or equal to the default) must not silently
// collapse map entries — the first registration wins, preserving revocation.
func TestAddDeviceTokenCollision(t *testing.T) {
	s := &Server{tokens: map[string]string{"shared": "default"}}
	s.AddDeviceToken("phone", "shared") // collides with the default token
	if s.tokens["shared"] != "default" {
		t.Fatal("collision must keep the first registration")
	}
	s.AddDeviceToken("ipad", "ipadtok")
	if s.tokens["ipadtok"] != "ipad" || len(s.tokens) != 2 {
		t.Fatalf("distinct token should register; tokens=%v", s.tokens)
	}
}

// remoteTurnPolicy must pin every remote turn to workspace-write. "auto" only
// swaps in auto_review when the operator opted in, "full" may reduce repeated
// prompts with on-failure but must not loosen the sandbox, and "custom" is refused.
func TestRemoteTurnPolicy(t *testing.T) {
	t.Run("default keeps human gate", func(t *testing.T) {
		p := remoteTurnPolicy("default", false)
		if p.sandboxMode != "workspace-write" || p.approvalPolicy != "on-request" || p.reviewer != "" {
			t.Fatalf("default = %+v", p)
		}
	})
	t.Run("auto without opt-in falls back to human gate", func(t *testing.T) {
		p := remoteTurnPolicy("auto", false)
		if p.reviewer != "" || p.approvalPolicy != "on-request" {
			t.Fatalf("auto must not enable auto_review without opt-in: %+v", p)
		}
	})
	t.Run("auto with opt-in enables auto_review", func(t *testing.T) {
		if p := remoteTurnPolicy("auto", true); p.reviewer != "auto_review" {
			t.Fatalf("auto with opt-in must enable auto_review: %+v", p)
		}
	})
	t.Run("full reduces prompts but stays sandboxed", func(t *testing.T) {
		p := remoteTurnPolicy("full", false)
		if p.approvalPolicy != "on-failure" || p.sandboxMode != "workspace-write" {
			t.Fatalf("full must stay workspace-write with on-failure: %+v", p)
		}
	})
	t.Run("custom is clamped to workspace-write", func(t *testing.T) {
		p := remoteTurnPolicy("custom", true)
		if !p.clamped || p.sandboxMode != "workspace-write" || p.reviewer != "" {
			t.Fatalf("custom = %+v", p)
		}
	})
}

// validateCwd must accept a project subdir under the workspace root but reject the
// home root itself, sensitive subtrees, relative/missing paths, and dirs outside
// the allowed roots. Empty is allowed (codex's own default).
func TestValidateCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_WORKSPACE_ROOTS", "") // default to $HOME
	realHome, _ := filepath.EvalSymlinks(home)

	mkdir := func(rel string) string {
		p := filepath.Join(realHome, rel)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	proj := mkdir("dev/project")

	t.Run("empty allowed", func(t *testing.T) {
		if c, err := validateCwd(""); err != nil || c != "" {
			t.Fatalf("empty cwd: %q,%v", c, err)
		}
	})
	t.Run("project subdir allowed", func(t *testing.T) {
		c, err := validateCwd(proj)
		if err != nil {
			t.Fatalf("project subdir rejected: %v", err)
		}
		if rp, _ := filepath.EvalSymlinks(proj); c != rp {
			t.Fatalf("got %q want %q", c, rp)
		}
	})
	reject := []struct{ name, in string }{
		{"home root", realHome},
		{"ssh dir", mkdir(".ssh")},
		{"codex dir", mkdir(".codex")},
		{"relative path", "dev/project"},
		{"missing dir", filepath.Join(realHome, "nope")},
		{"outside workspace root", t.TempDir()},
	}
	for _, c := range reject {
		t.Run("reject "+c.name, func(t *testing.T) {
			if got, err := validateCwd(c.in); err == nil {
				t.Fatalf("expected rejection, got %q", got)
			}
		})
	}
}

// looksLikeMedia is the magic-byte sniff backing the /file content gate: real
// image/video signatures pass; creds/source/text (renamed to a media name) fail.
func TestLooksLikeMedia(t *testing.T) {
	media := map[string][]byte{
		"png":  {0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A},
		"jpeg": {0xFF, 0xD8, 0xFF, 0xE0},
		"gif":  []byte("GIF89a.."),
		"webp": []byte("RIFF\x00\x00\x00\x00WEBPVP8 "),
		"bmp":  []byte("BM\x00\x00"),
		"mp4":  []byte("\x00\x00\x00\x18ftypmp42"),
		"webm": {0x1A, 0x45, 0xDF, 0xA3},
	}
	for name, b := range media {
		if !looksLikeMedia(b) {
			t.Errorf("%s should look like media", name)
		}
	}
	notMedia := map[string][]byte{
		"pem":   []byte("-----BEGIN OPENSSH PRIVATE KEY-----"),
		"json":  []byte(`{"access_token":"x"}`),
		"text":  []byte("root:x:0:0:root:/root:/bin/sh\n"),
		"empty": {},
		"short": {0x89, 'P'},
		"elf":   {0x7F, 'E', 'L', 'F'},
	}
	for name, b := range notMedia {
		if looksLikeMedia(b) {
			t.Errorf("%s should NOT look like media", name)
		}
	}
}

// dirIsOrUnder backs the cwd denylist and MUST match case-insensitively so a
// case-insensitive filesystem (macOS/Windows) can't dodge a protected root by
// changing case (~/.SSH == ~/.ssh). Tested at the string level (no FS needed).
func TestDirIsOrUnderCaseInsensitive(t *testing.T) {
	sep := string(os.PathSeparator)
	base := sep + "Users" + sep + "v" + sep + ".ssh"
	match := []string{
		sep + "Users" + sep + "v" + sep + ".ssh",                  // exact
		sep + "Users" + sep + "v" + sep + ".SSH",                  // case-folded exact
		sep + "Users" + sep + "v" + sep + ".ssh" + sep + "keys",   // subtree
		sep + "Users" + sep + "v" + sep + ".SSH" + sep + "id_rsa", // case-folded subtree
	}
	for _, m := range match {
		if !dirIsOrUnder(m, base) {
			t.Errorf("dirIsOrUnder(%q,%q) = false, want true", m, base)
		}
	}
	noMatch := []string{
		sep + "Users" + sep + "v" + sep + ".ssh-backup", // sibling prefix, not a subtree
		sep + "Users" + sep + "v" + sep + "dev",         // unrelated
	}
	for _, m := range noMatch {
		if dirIsOrUnder(m, base) {
			t.Errorf("dirIsOrUnder(%q,%q) = true, want false", m, base)
		}
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A thread whose tail exceeds the byte budget must shrink its turn window until
// the frame fits (mirrors the read handler's loop).
func TestTailThreadByteBoundShrinks(t *testing.T) {
	big := strings.Repeat("x", 1<<20) // 1MB per turn
	turns := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		turns = append(turns, `{"pad":"`+big+`"}`)
	}
	raw := json.RawMessage(`{"thread":{"turns":[` + strings.Join(turns, ",") + `]}}`)

	sliced, _, total := tailThread(raw, 0)
	if total != 12 {
		t.Fatalf("total=%d, want 12", total)
	}
	cur := total
	for len(sliced) > maxThreadBytes && cur > 1 {
		cur /= 2
		sliced, _, _ = tailThread(raw, cur)
	}
	if len(sliced) > maxThreadBytes {
		t.Fatalf("frame still %d bytes after shrink (cur=%d)", len(sliced), cur)
	}
	if cur >= total {
		t.Fatalf("window did not shrink: cur=%d total=%d", cur, total)
	}
}
