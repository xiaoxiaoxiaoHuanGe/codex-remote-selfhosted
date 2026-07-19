package hub

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yunyuchen/codex-remote/bridge/internal/license"
)

const testAdminKey = "adminsecret"

// newAdminServer: license 模式 hub + 它的 admin API 测试监听。
func newAdminServer(t *testing.T) (*Hub, *httptest.Server, *license.Service) {
	t.Helper()
	svc, err := license.Open(filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatalf("license.Open: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	svc.Now = func() time.Time { return licBase }
	h := New(testAgentKey)
	h.Lic = svc
	srv := httptest.NewServer(h.AdminHandler(testAdminKey))
	t.Cleanup(srv.Close)
	return h, srv, svc
}

// adminReq fires one authenticated request; key=="" omits the header.
func adminReq(t *testing.T, srv *httptest.Server, method, path, key string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, srv.URL+path, rd)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, data
}

func TestAdminAuthGate(t *testing.T) {
	_, srv, _ := newAdminServer(t)
	for _, path := range []string{"/admin/keys", "/admin/machines", "/admin/status"} {
		if code, _ := adminReq(t, srv, http.MethodGet, path, "", nil); code != http.StatusUnauthorized {
			t.Fatalf("%s no bearer: code=%d, want 401", path, code)
		}
		if code, _ := adminReq(t, srv, http.MethodGet, path, "wrong-key", nil); code != http.StatusUnauthorized {
			t.Fatalf("%s wrong bearer: code=%d, want 401", path, code)
		}
	}
}

func TestAdminLicenseModeOff(t *testing.T) {
	h := New(testAgentKey) // Lic == nil（自托管模式）
	srv := httptest.NewServer(h.AdminHandler(testAdminKey))
	t.Cleanup(srv.Close)
	if code, _ := adminReq(t, srv, http.MethodGet, "/admin/keys", testAdminKey, nil); code != http.StatusNotFound {
		t.Fatalf("keys without Lic: code=%d, want 404", code)
	}
	// status 不依赖 license，自托管也可用
	if code, _ := adminReq(t, srv, http.MethodGet, "/admin/status", testAdminKey, nil); code != http.StatusOK {
		t.Fatalf("status without Lic: code=%d, want 200", code)
	}
}

func TestAdminKeyLifecycle(t *testing.T) {
	_, srv, _ := newAdminServer(t)

	// 发卡：明文 key 仅此一次
	code, body := adminReq(t, srv, http.MethodPost, "/admin/keys", testAdminKey,
		map[string]any{"plan": "beta", "machineLimit": 2, "months": 6})
	if code != http.StatusOK {
		t.Fatalf("issue: code=%d body=%s", code, body)
	}
	var issued struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(body, &issued); err != nil || !license.ValidKeyFormat(issued.Key) {
		t.Fatalf("issue body=%s err=%v", body, err)
	}

	// 列表
	code, body = adminReq(t, srv, http.MethodGet, "/admin/keys", testAdminKey, nil)
	if code != http.StatusOK {
		t.Fatalf("list: code=%d", code)
	}
	var keys []struct {
		Prefix    string `json:"prefix"`
		Plan      string `json:"plan"`
		Limit     int    `json:"limit"`
		Used      int    `json:"used"`
		ExpiresAt string `json:"expiresAt"`
		Revoked   bool   `json:"revoked"`
	}
	if err := json.Unmarshal(body, &keys); err != nil || len(keys) != 1 {
		t.Fatalf("list body=%s err=%v", body, err)
	}
	if keys[0].Prefix != issued.Key[:12] || keys[0].Plan != "beta" || keys[0].Used != 0 {
		t.Fatalf("row = %+v", keys[0])
	}

	// 续期：licBase 发 6 个月，再续 6 → 共 12 个月
	code, _ = adminReq(t, srv, http.MethodPost, "/admin/keys/extend", testAdminKey,
		map[string]any{"prefix": issued.Key[:12], "months": 6})
	if code != http.StatusOK {
		t.Fatalf("extend: code=%d", code)
	}
	code, body = adminReq(t, srv, http.MethodGet, "/admin/keys", testAdminKey, nil)
	if code != http.StatusOK {
		t.Fatalf("list2: code=%d", code)
	}
	if err := json.Unmarshal(body, &keys); err != nil {
		t.Fatalf("list2: %v", err)
	}
	wantExp := licBase.AddDate(0, 12, 0).Format(time.RFC3339)
	if keys[0].ExpiresAt != wantExp {
		t.Fatalf("expiresAt = %s, want %s", keys[0].ExpiresAt, wantExp)
	}

	// 吊销
	code, _ = adminReq(t, srv, http.MethodPost, "/admin/keys/revoke", testAdminKey,
		map[string]any{"prefix": issued.Key[:12]})
	if code != http.StatusOK {
		t.Fatalf("revoke: code=%d", code)
	}
	_, body = adminReq(t, srv, http.MethodGet, "/admin/keys", testAdminKey, nil)
	if err := json.Unmarshal(body, &keys); err != nil || !keys[0].Revoked {
		t.Fatalf("after revoke: body=%s", body)
	}

	// 未知前缀 → 404；参数缺失 → 400
	code, _ = adminReq(t, srv, http.MethodPost, "/admin/keys/revoke", testAdminKey,
		map[string]any{"prefix": "crk_nosuchpfx"})
	if code != http.StatusNotFound {
		t.Fatalf("revoke unknown: code=%d, want 404", code)
	}
	code, _ = adminReq(t, srv, http.MethodPost, "/admin/keys", testAdminKey,
		map[string]any{"plan": "", "machineLimit": 0, "months": 0})
	if code != http.StatusBadRequest {
		t.Fatalf("issue bad args: code=%d, want 400", code)
	}
}

func TestAdminKeysEmptyListIsArray(t *testing.T) {
	_, srv, _ := newAdminServer(t)
	_, body := adminReq(t, srv, http.MethodGet, "/admin/keys", testAdminKey, nil)
	if strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("empty list = %q, want []", body)
	}
}

func TestAdminMachinesAndUnbind(t *testing.T) {
	_, srv, svc := newAdminServer(t)
	key, err := svc.IssueKey("beta", 2, 6)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := svc.Activate(key, "m-studio", "Mac Studio", ""); err != nil {
		t.Fatalf("activate: %v", err)
	}

	code, body := adminReq(t, srv, http.MethodGet, "/admin/machines", testAdminKey, nil)
	if code != http.StatusOK {
		t.Fatalf("machines: code=%d", code)
	}
	var ms []struct {
		MachineID string `json:"machineId"`
		Name      string `json:"name"`
		KeyPrefix string `json:"keyPrefix"`
		Online    bool   `json:"online"`
		Phones    int    `json:"phones"`
	}
	if err := json.Unmarshal(body, &ms); err != nil || len(ms) != 1 {
		t.Fatalf("machines body=%s err=%v", body, err)
	}
	if ms[0].MachineID != "m-studio" || ms[0].KeyPrefix != key[:12] || ms[0].Online {
		t.Fatalf("row = %+v", ms[0])
	}

	// 解绑 → 名册清空
	code, _ = adminReq(t, srv, http.MethodPost, "/admin/machines/unbind", testAdminKey,
		map[string]any{"machineId": "m-studio"})
	if code != http.StatusOK {
		t.Fatalf("unbind: code=%d", code)
	}
	_, body = adminReq(t, srv, http.MethodGet, "/admin/machines", testAdminKey, nil)
	if err := json.Unmarshal(body, &ms); err != nil || len(ms) != 0 {
		t.Fatalf("after unbind body=%s", body)
	}

	// 再解绑同一台 → 404
	code, _ = adminReq(t, srv, http.MethodPost, "/admin/machines/unbind", testAdminKey,
		map[string]any{"machineId": "m-studio"})
	if code != http.StatusNotFound {
		t.Fatalf("unbind again: code=%d, want 404", code)
	}
}

func TestAdminStatusShape(t *testing.T) {
	_, srv, _ := newAdminServer(t)
	code, body := adminReq(t, srv, http.MethodGet, "/admin/status", testAdminKey, nil)
	if code != http.StatusOK {
		t.Fatalf("status: code=%d", code)
	}
	var st struct {
		Uptime        *int   `json:"uptime"`
		GoVersion     string `json:"goVersion"`
		AgentsOnline  int    `json:"agentsOnline"`
		PhoneSessions int    `json:"phoneSessions"`
		Agents        []any  `json:"agents"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatalf("status body=%s err=%v", body, err)
	}
	if st.Uptime == nil || st.GoVersion == "" || st.AgentsOnline != 0 || st.Agents == nil {
		t.Fatalf("status = %s", body)
	}
}

func TestAdminKeyDelete(t *testing.T) {
	_, srv, svc := newAdminServer(t)
	key, err := svc.IssueKey("beta", 2, 6)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := svc.Activate(key, "m-del", "Del Mac", ""); err != nil {
		t.Fatalf("activate: %v", err)
	}
	// 删除存在的卡
	code, _ := adminReq(t, srv, http.MethodPost, "/admin/keys/delete", testAdminKey,
		map[string]any{"prefix": key[:12]})
	if code != http.StatusOK {
		t.Fatalf("delete: code=%d", code)
	}
	// 卡密列表与名册都空
	_, body := adminReq(t, srv, http.MethodGet, "/admin/keys", testAdminKey, nil)
	if strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("keys after delete = %q", body)
	}
	// 删未知前缀 → 404
	code, _ = adminReq(t, srv, http.MethodPost, "/admin/keys/delete", testAdminKey,
		map[string]any{"prefix": "crk_nosuchpfx"})
	if code != http.StatusNotFound {
		t.Fatalf("delete unknown: code=%d, want 404", code)
	}
}

func TestAdminMachineBlockUnblock(t *testing.T) {
	_, srv, svc := newAdminServer(t)
	key, err := svc.IssueKey("beta", 2, 6)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := svc.Activate(key, "m-blk", "Blk Mac", ""); err != nil {
		t.Fatalf("activate: %v", err)
	}
	// 拉黑
	code, _ := adminReq(t, srv, http.MethodPost, "/admin/machines/block", testAdminKey,
		map[string]any{"machineId": "m-blk"})
	if code != http.StatusOK {
		t.Fatalf("block: code=%d", code)
	}
	// 名册显示 blocked
	_, body := adminReq(t, srv, http.MethodGet, "/admin/machines", testAdminKey, nil)
	var ms []struct {
		MachineID string `json:"machineId"`
		Blocked   bool   `json:"blocked"`
	}
	if err := json.Unmarshal(body, &ms); err != nil || len(ms) != 1 || !ms[0].Blocked {
		t.Fatalf("machines blocked body=%s err=%v", body, err)
	}
	// 解黑
	code, _ = adminReq(t, srv, http.MethodPost, "/admin/machines/unblock", testAdminKey,
		map[string]any{"machineId": "m-blk"})
	if code != http.StatusOK {
		t.Fatalf("unblock: code=%d", code)
	}
	_, body = adminReq(t, srv, http.MethodGet, "/admin/machines", testAdminKey, nil)
	if err := json.Unmarshal(body, &ms); err != nil || ms[0].Blocked {
		t.Fatalf("after unblock body=%s", body)
	}
	// 拉黑未知机器 → 404
	code, _ = adminReq(t, srv, http.MethodPost, "/admin/machines/block", testAdminKey,
		map[string]any{"machineId": "nope"})
	if code != http.StatusNotFound {
		t.Fatalf("block unknown: code=%d, want 404", code)
	}
}
