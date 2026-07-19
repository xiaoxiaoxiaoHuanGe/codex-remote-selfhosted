package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/yunyuchen/codex-remote/bridge/internal/license"
)

// licBase pins every license-mode hub test to a fixed clock so expiry/grace
// assertions are deterministic regardless of when the suite runs.
var licBase = time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)

// newLicensedHub: hub in license mode with one issued key (limit 2, 6 months
// from licBase) and one activated machine (testMachine).
func newLicensedHub(t *testing.T) (*Hub, *httptest.Server, *license.Service, string, string) {
	t.Helper()
	svc, err := license.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatalf("license.Open: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	svc.Now = func() time.Time { return licBase }
	key, err := svc.IssueKey("beta", 2, 6)
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}
	res, err := svc.Activate(key, testMachine, "Test Mac", "fp1")
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	h := New(testAgentKey)
	h.Lic = svc
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)
	return h, srv, svc, key, res.Credential
}

// dialRegister sends one register frame and returns the conn + first reply.
func dialRegister(t *testing.T, srv *httptest.Server, reg frame) (*websocket.Conn, frame) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, wsBase(t, srv)+"/agent", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close(websocket.StatusNormalClosure, "") })
	if err := wsjson.Write(ctx, ws, reg); err != nil {
		t.Fatalf("write register: %v", err)
	}
	var reply frame
	if err := wsjson.Read(ctx, ws, &reply); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	return ws, reply
}

func TestLicenseRegisterValidCred(t *testing.T) {
	_, srv, _, _, cred := newLicensedHub(t)
	_, reply := dialRegister(t, srv, frame{T: "register", MachineID: testMachine, Token: "tok1", Cred: cred})
	if reply.T != "registered" {
		t.Fatalf("want registered, got %+v", reply)
	}
}

func TestLicenseRegisterBadCredRejected(t *testing.T) {
	_, srv, _, _, _ := newLicensedHub(t)
	_, reply := dialRegister(t, srv, frame{T: "register", MachineID: testMachine, Token: "tok1", Cred: "mc_wrong"})
	if reply.T != "error" {
		t.Fatalf("want error, got %+v", reply)
	}
}

func TestLicenseRegisterSharedAgentKeyIgnored(t *testing.T) {
	// license mode accepts ONLY machine credentials — the shared key is dead there
	_, srv, _, _, _ := newLicensedHub(t)
	_, reply := dialRegister(t, srv, frame{T: "register", MachineID: testMachine, Token: "tok1", AgentKey: testAgentKey})
	if reply.T != "error" {
		t.Fatalf("shared key must not register in license mode, got %+v", reply)
	}
}

func postJSON(t *testing.T, url string, body string) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp, m
}

func TestActivateEndpoint(t *testing.T) {
	_, srv, _, key, _ := newLicensedHub(t) // limit 2, one slot used by testMachine
	resp, m := postJSON(t, srv.URL+"/api/activate",
		`{"key":"`+key+`","machineId":"m9","name":"Nine","fpHint":"fp9"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %v", resp.StatusCode, m)
	}
	cred, _ := m["credential"].(string)
	if !strings.HasPrefix(cred, "mc_") || m["used"].(float64) != 2 || m["limit"].(float64) != 2 {
		t.Fatalf("bad body: %v", m)
	}
	// roster is now full
	resp, m = postJSON(t, srv.URL+"/api/activate",
		`{"key":"`+key+`","machineId":"m10","fpHint":"fp10"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("full roster: status %d %v", resp.StatusCode, m)
	}
	// bad key
	resp, _ = postJSON(t, srv.URL+"/api/activate", `{"key":"crk_bad","machineId":"m11"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bad key: status %d", resp.StatusCode)
	}
}

func TestActivateEndpoint404WhenNoLicense(t *testing.T) {
	h := New(testAgentKey) // Lic == nil: self-host mode, surface unchanged
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)
	resp, _ := postJSON(t, srv.URL+"/api/activate", `{}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestRosterEndpointLegacyTokenAuth(t *testing.T) {
	_, srv, svc, key, cred := newLicensedHub(t)
	// the machine must be ONLINE for legacy token auth (findAgent resolves
	// among connected agents)
	dialRegister(t, srv, frame{T: "register", MachineID: testMachine, Token: "tok1", Cred: cred})
	if _, err := svc.Activate(key, "m2", "PC", "fp2"); err != nil {
		t.Fatalf("activate m2: %v", err)
	}

	resp, err := http.Get(srv.URL + "/api/roster?machine=" + testMachine + "&token=tok1")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("roster: %v status=%d", err, resp.StatusCode)
	}
	var ms []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&ms)
	_ = resp.Body.Close()
	if len(ms) != 2 {
		t.Fatalf("want 2 machines, got %v", ms)
	}
	// wrong token → 401
	resp, _ = http.Get(srv.URL + "/api/roster?machine=" + testMachine + "&token=nope")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: want 401, got %d", resp.StatusCode)
	}
}

func TestRosterDeactivateKicksAgent(t *testing.T) {
	_, srv, _, _, cred := newLicensedHub(t)
	ws, _ := dialRegister(t, srv, frame{T: "register", MachineID: testMachine, Token: "tok1", Cred: cred})

	resp, m := postJSON(t, srv.URL+"/api/roster/deactivate?machine="+testMachine+"&token=tok1",
		`{"machineId":"`+testMachine+`"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("deactivate: status %d %v", resp.StatusCode, m)
	}
	// the kicked agent's read loop must error out promptly
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var f frame
	if err := wsjson.Read(ctx, ws, &f); err == nil {
		t.Fatalf("agent still alive after deactivation: %+v", f)
	}
	// unknown machine: after kicking testMachine the legacy-auth machine is
	// offline, so this must NOT succeed (401 offline or 404 unknown both fine)
	resp, _ = postJSON(t, srv.URL+"/api/roster/deactivate?machine="+testMachine+"&token=tok1", `{"machineId":"ghost"}`)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("ghost deactivate succeeded: status %d", resp.StatusCode)
	}
}

func TestRecheckKicksRevokedAndStaleCred(t *testing.T) {
	h, srv, svc, key, cred := newLicensedHub(t)
	ws, _ := dialRegister(t, srv, frame{T: "register", MachineID: testMachine, Token: "tok1", Cred: cred})

	// healthy recheck: nothing happens
	h.recheckOnce()
	// revoke → next recheck kicks the live link
	if err := svc.RevokeKey(key); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	h.recheckOnce()
	// no other frames are ever sent to an idle agent, so the very next read
	// MUST be the close error from the kick
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var f frame
	if err := wsjson.Read(ctx, ws, &f); err == nil {
		t.Fatalf("agent survived revocation recheck: %+v", f)
	}
}

func TestLicenseRegisterGraceSendsNotice(t *testing.T) {
	_, srv, svc, _, cred := newLicensedHub(t)
	// key expires licBase+6mo; one day past expiry is inside the 72h grace
	svc.Now = func() time.Time { return licBase.AddDate(0, 6, 0).Add(24 * time.Hour) }
	ws, reply := dialRegister(t, srv, frame{T: "register", MachineID: testMachine, Token: "tok1", Cred: cred})
	if reply.T != "registered" {
		t.Fatalf("grace should register, got %+v", reply)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var notice frame
	if err := wsjson.Read(ctx, ws, &notice); err != nil || notice.T != "notice" || notice.Message == "" {
		t.Fatalf("want notice frame, got %+v err=%v", notice, err)
	}
}
