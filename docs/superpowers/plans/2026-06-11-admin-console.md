# Web 后台管理实施计划（卡密运营 + 中继状态）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给 codex-remote 的订阅控制面配一个 Web 后台（发卡/吊销/续期/解绑 + 中继实时状态），替代 SSH + CLI。

**Architecture:** RuoYi-Vue 开源后端原样部署（框架功能零开发）；codexhub 新开仅回环监听的内部 admin API（Bearer 共享密钥）；RuoYi 侧一个无状态 Java 薄代理包转发；前端用 ele-admin-plus-ruoyi-ts-pro 模板加三个页面。数据所有权不变：卡密在 hub 的 SQLite，实时状态在 hub 内存，MySQL 只存 RuoYi 框架数据。

**Tech Stack:** Go（modernc sqlite、coder/websocket）、RuoYi-Vue v3.8.8（Spring Boot + fastjson2，JDK 8）、Vue 3 + TS + Element Plus（ele-admin-plus）。

**Spec:** `docs/superpowers/specs/2026-06-11-admin-console-design.md`

---

## 文件地图

| 文件 | 动作 | 职责 |
|---|---|---|
| `bridge/internal/license/license.go` | 改 | 新增 `ErrPrefixAmbiguous` |
| `bridge/internal/license/admin.go` | 改 | `subByPrefix`/`RevokeKeyByPrefix`/`ExtendKeyByPrefix`/`extendAccount` |
| `bridge/internal/license/roster.go` | 改 | `AdminMachineInfo`/`ListMachines` |
| `bridge/internal/license/prefix_test.go` | 建 | ByPrefix 函数测试 |
| `bridge/internal/license/roster_test.go` | 改 | `ListMachines` 测试 |
| `bridge/internal/hub/hub.go` | 改 | `Hub.started`、`agent.connectedAt` 两个字段 |
| `bridge/internal/hub/admin.go` | 建 | 操作员 admin API（独立 Handler） |
| `bridge/internal/hub/admin_test.go` | 建 | admin API 测试 |
| `bridge/cmd/codexhub/main.go` | 改 | `HUB_ADMIN_ADDR`/`HUB_ADMIN_KEY` 接线 + 包注释 |
| `admin/ruoyi-ext/src/main/java/com/ruoyi/caret/*.java` | 建 | Java 薄代理（4 个类） |
| `admin/ruoyi-ext/sql/caret_menu.sql` | 建 | RuoYi 菜单/权限 |
| `admin/ruoyi-ext/application-caret.yml` | 建 | hub 对接配置样例 |
| `admin/ruoyi-ext/README.md` | 建 | drop-in 部署说明（锁定上游 tag） |
| `admin/web/` | 建 | 前端模板落库 + `src/api/caret/*` + `src/views/caret/*` |

---

## Phase 1 — Go：license 包新函数

### Task 1: 按前缀吊销/续期

**Files:**
- Modify: `bridge/internal/license/license.go`（errors 块）
- Modify: `bridge/internal/license/admin.go`
- Create: `bridge/internal/license/prefix_test.go`

- [ ] **Step 1: 写失败测试**

新建 `bridge/internal/license/prefix_test.go`（`tempSvc` 是 `store_test.go` 里现成的包级辅助）：

```go
package license

import (
	"errors"
	"testing"
	"time"
)

func TestRevokeKeyByPrefix(t *testing.T) {
	svc := tempSvc(t)
	key, err := svc.IssueKey("beta", 2, 6)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	if err := svc.RevokeKeyByPrefix("crk_nosuchpfx"); !errors.Is(err, ErrBadKey) {
		t.Fatalf("unknown prefix: got %v, want ErrBadKey", err)
	}
	if err := svc.RevokeKeyByPrefix(key[:12]); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	keys, err := svc.ListKeys()
	if err != nil || len(keys) != 1 {
		t.Fatalf("list: err=%v len=%d", err, len(keys))
	}
	if !keys[0].Revoked {
		t.Fatal("key not marked revoked")
	}
}

func TestExtendKeyByPrefix(t *testing.T) {
	svc := tempSvc(t)
	base := time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return base }
	key, err := svc.IssueKey("beta", 2, 1) // 到期 2026-07-11
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := svc.ExtendKeyByPrefix(key[:12], 2); err != nil {
		t.Fatalf("extend: %v", err)
	}
	keys, err := svc.ListKeys()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := base.AddDate(0, 3, 0) // 发卡 1 个月 + 续期 2 个月
	if !keys[0].ExpiresAt.Equal(want) {
		t.Fatalf("expiry = %v, want %v", keys[0].ExpiresAt, want)
	}
}

func TestByPrefixAmbiguous(t *testing.T) {
	svc := tempSvc(t)
	k1, err := svc.IssueKey("beta", 1, 1)
	if err != nil {
		t.Fatalf("issue k1: %v", err)
	}
	k2, err := svc.IssueKey("beta", 1, 1)
	if err != nil {
		t.Fatalf("issue k2: %v", err)
	}
	// 真实 key 几乎不可能撞 12 位前缀，手工把第二行改成第一行的前缀制造歧义。
	if _, err := svc.db.Exec(`UPDATE license_keys SET prefix=? WHERE key_hash=?`,
		k1[:12], hashSecret(k2)); err != nil {
		t.Fatalf("force collision: %v", err)
	}
	if err := svc.RevokeKeyByPrefix(k1[:12]); !errors.Is(err, ErrPrefixAmbiguous) {
		t.Fatalf("revoke: got %v, want ErrPrefixAmbiguous", err)
	}
	if err := svc.ExtendKeyByPrefix(k1[:12], 1); !errors.Is(err, ErrPrefixAmbiguous) {
		t.Fatalf("extend: got %v, want ErrPrefixAmbiguous", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd bridge && go test ./internal/license/ -run 'ByPrefix' -v`
Expected: 编译失败 `undefined: ErrPrefixAmbiguous`（及 ByPrefix 函数未定义）

- [ ] **Step 3: 最小实现**

`bridge/internal/license/license.go` 的 `var (...)` errors 块末尾加一行：

```go
	ErrPrefixAmbiguous = errors.New("license: prefix matches multiple keys")
```

`bridge/internal/license/admin.go` 末尾追加：

```go
// subByPrefix resolves a key row by display prefix — the web-admin path, where
// the plaintext key no longer exists anywhere. Ambiguity (two keys sharing a
// prefix) is refused; disambiguate with the full key via the CLI.
func (s *Service) subByPrefix(prefix string) (subRow, error) {
	rows, err := s.db.Query(`
		SELECT k.account_id, k.revoked, s.plan, s.machine_limit, s.status, s.expires_at
		FROM license_keys k JOIN subscriptions s ON s.account_id = k.account_id
		WHERE k.prefix = ?`, prefix)
	if err != nil {
		return subRow{}, err
	}
	defer rows.Close()
	var out []subRow
	for rows.Next() {
		var r subRow
		var exp int64
		var revoked int
		if err := rows.Scan(&r.AccountID, &revoked, &r.Plan, &r.MachineLimit, &r.Status, &exp); err != nil {
			return subRow{}, err
		}
		r.ExpiresAt = time.Unix(exp, 0).UTC()
		r.Revoked = revoked != 0
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return subRow{}, err
	}
	switch len(out) {
	case 0:
		return subRow{}, ErrBadKey
	case 1:
		return out[0], nil
	default:
		return subRow{}, ErrPrefixAmbiguous
	}
}

// RevokeKeyByPrefix is RevokeKey for the web admin (see subByPrefix).
func (s *Service) RevokeKeyByPrefix(prefix string) error {
	if _, err := s.subByPrefix(prefix); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE license_keys SET revoked=1 WHERE prefix=?`, prefix)
	return err
}

// ExtendKeyByPrefix is ExtendKey for the web admin (see subByPrefix).
func (s *Service) ExtendKeyByPrefix(prefix string, months int) error {
	sub, err := s.subByPrefix(prefix)
	if err != nil {
		return err
	}
	return s.extendAccount(sub.AccountID, sub.ExpiresAt, months)
}

// extendAccount pushes expiry out by months from max(now, current expiry) and
// re-activates the subscription status. Shared by both Extend paths.
func (s *Service) extendAccount(accountID int64, cur time.Time, months int) error {
	base := cur
	if now := s.Now().UTC(); now.After(base) {
		base = now
	}
	_, err := s.db.Exec(
		`UPDATE subscriptions SET expires_at=?, status='active' WHERE account_id=?`,
		base.AddDate(0, months, 0).Unix(), accountID)
	return err
}
```

同文件中把现有 `ExtendKey` 的函数体改为复用 `extendAccount`（替换 `base := sub.ExpiresAt` 起到函数结尾的部分）：

```go
// ExtendKey pushes expiry out by months from max(now, current expiry) and
// re-activates the subscription status.
func (s *Service) ExtendKey(key string, months int) error {
	sub, err := s.accountByKeyAnyState(key)
	if err != nil {
		return err
	}
	return s.extendAccount(sub.AccountID, sub.ExpiresAt, months)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd bridge && go test ./internal/license/ -v`
Expected: 全部 PASS（含原有测试——`ExtendKey` 重构不得破坏 `admin_test.go`）

- [ ] **Step 5: Commit**

```bash
git add bridge/internal/license/
git commit -m "feat(license): 按前缀吊销/续期 —— web 后台只有前缀可用"
```

### Task 2: 全局机器列表 ListMachines

**Files:**
- Modify: `bridge/internal/license/roster.go`
- Modify: `bridge/internal/license/roster_test.go`

- [ ] **Step 1: 写失败测试**

`bridge/internal/license/roster_test.go` 末尾追加：

```go
func TestListMachines(t *testing.T) {
	svc := tempSvc(t)
	key, err := svc.IssueKey("beta", 2, 6)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := svc.Activate(key, "m-studio", "Mac Studio", ""); err != nil {
		t.Fatalf("activate: %v", err)
	}

	ms, err := svc.ListMachines()
	if err != nil || len(ms) != 1 {
		t.Fatalf("list: err=%v len=%d", err, len(ms))
	}
	m := ms[0]
	if m.MachineID != "m-studio" || m.Name != "Mac Studio" || m.KeyPrefix != key[:12] {
		t.Fatalf("row = %+v", m)
	}
	if m.ActivatedAt.IsZero() {
		t.Fatal("ActivatedAt zero")
	}

	if err := svc.AdminDeactivate("m-studio"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	ms, err = svc.ListMachines()
	if err != nil || len(ms) != 0 {
		t.Fatalf("after deactivate: err=%v len=%d", err, len(ms))
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd bridge && go test ./internal/license/ -run TestListMachines -v`
Expected: 编译失败 `svc.ListMachines undefined`

- [ ] **Step 3: 最小实现**

`bridge/internal/license/roster.go` 末尾追加：

```go
// AdminMachineInfo is one row of the operator's GLOBAL machine list (web
// admin): the active roster entry plus which key (by display prefix) owns it.
type AdminMachineInfo struct {
	MachineID   string
	Name        string
	KeyPrefix   string
	ActivatedAt time.Time
	LastSeen    time.Time // zero when the machine never registered
}

// ListMachines lists every active machine across all accounts, oldest first.
// Phase-1 invariant: IssueKey makes accounts and keys 1:1, so the join is flat.
func (s *Service) ListMachines() ([]AdminMachineInfo, error) {
	rows, err := s.db.Query(`
		SELECT m.machine_id, m.name, k.prefix, m.activated_at, COALESCE(m.last_seen, 0)
		FROM machines m JOIN license_keys k ON k.account_id = m.account_id
		WHERE m.deactivated_at IS NULL
		ORDER BY m.activated_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminMachineInfo
	for rows.Next() {
		var m AdminMachineInfo
		var act, seen int64
		if err := rows.Scan(&m.MachineID, &m.Name, &m.KeyPrefix, &act, &seen); err != nil {
			return nil, err
		}
		m.ActivatedAt = time.Unix(act, 0).UTC()
		if seen != 0 {
			m.LastSeen = time.Unix(seen, 0).UTC()
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd bridge && go test ./internal/license/ -v`
Expected: 全部 PASS

- [ ] **Step 5: Commit**

```bash
git add bridge/internal/license/roster.go bridge/internal/license/roster_test.go
git commit -m "feat(license): ListMachines 全局机器名册（含所属卡密前缀）"
```

## Phase 2 — Go：hub admin API

### Task 3: 字段铺垫 + AdminHandler 认证骨架

**Files:**
- Modify: `bridge/internal/hub/hub.go`（两个字段）
- Create: `bridge/internal/hub/admin.go`
- Create: `bridge/internal/hub/admin_test.go`

- [ ] **Step 1: 写失败测试**

新建 `bridge/internal/hub/admin_test.go`（`licBase`/`testAgentKey` 是包内现成常量）：

```go
package hub

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd bridge && go test ./internal/hub/ -run TestAdmin -v`
Expected: 编译失败 `h.AdminHandler undefined`

- [ ] **Step 3: hub.go 加两个字段**

`bridge/internal/hub/hub.go`：

1. `Hub` 结构体（`agents   map[string]*agent` 之后）加：

```go
	started time.Time // process-lifetime anchor for the admin /admin/status uptime
```

2. `New` 函数改为：

```go
func New(agentKey string) *Hub {
	return &Hub{
		agentKey: agentKey,
		agents:   make(map[string]*agent),
		fileTix:  make(map[string]fileTicket),
		started:  time.Now(),
	}
}
```

3. `agent` 结构体（`devices map[string]string` 行之后）加：

```go
	connectedAt time.Time // when this link registered (admin status display)
```

4. `handleAgent` 里构造 agent 的字面量（`a := &agent{...}`，约 321 行）补一项：

```go
	a := &agent{
		id: reg.MachineID, name: reg.Name, token: reg.Token, cred: reg.Cred, ws: ws,
		phones: make(map[string]*phone), fileReqs: make(map[string]chan frame),
		devices: make(map[string]string), connectedAt: time.Now(),
	}
```

- [ ] **Step 4: 新建 admin.go（骨架 + status 占位完整实现）**

新建 `bridge/internal/hub/admin.go`：

```go
// admin.go — the OPERATOR control API, consumed by the RuoYi admin backend
// (web 后台). Unlike api.go (client-facing license endpoints on the public
// listener), this handler is served from its OWN listener (HUB_ADMIN_ADDR —
// loopback in production, never proxied by nginx) and every request must carry
// a Bearer shared secret (HUB_ADMIN_KEY) compared in constant time. Plaintext
// license keys appear exactly once — in the issue response — and are never
// logged or stored.
//
//	POST /admin/keys            {plan,machineLimit,months} → {key} (once!)
//	GET  /admin/keys            → [{prefix,plan,limit,used,expiresAt,revoked}]
//	POST /admin/keys/revoke     {prefix}
//	POST /admin/keys/extend     {prefix,months}
//	GET  /admin/machines        → roster ⨝ live link state (license mode only)
//	POST /admin/machines/unbind {machineId} — frees the slot + kicks the link
//	GET  /admin/status          → live agents + hub runtime stats
package hub

import (
	"encoding/json"
	"errors"
	"net/http"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/yunyuchen/codex-remote/bridge/internal/license"
)
```

（注意：`log` 此时**先不要**进 import —— Task 3 的代码还用不到它，Go 会报未使用 import；Task 4 换真实现时再加。）

```go

// AdminHandler builds the operator API mux. adminKey must be non-empty — the
// caller (cmd/codexhub) refuses to start the listener otherwise.
func (h *Hub) AdminHandler(adminKey string) http.Handler {
	auth := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || !ctEq(tok, adminKey) {
				writeAdminErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			fn(w, r)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/keys", auth(h.adminKeys))
	mux.HandleFunc("/admin/keys/revoke", auth(h.adminKeyRevoke))
	mux.HandleFunc("/admin/keys/extend", auth(h.adminKeyExtend))
	mux.HandleFunc("/admin/machines", auth(h.adminMachines))
	mux.HandleFunc("/admin/machines/unbind", auth(h.adminMachineUnbind))
	mux.HandleFunc("/admin/status", auth(h.adminStatus))
	return mux
}

func writeAdminErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// licErrStatus maps license errors onto the admin API's HTTP codes.
func licErrStatus(err error) int {
	switch {
	case errors.Is(err, license.ErrBadKey), errors.Is(err, license.ErrUnknownMachine):
		return http.StatusNotFound
	case errors.Is(err, license.ErrPrefixAmbiguous):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// fmtT renders a timestamp for the JSON API; zero → "" (machine never seen).
func fmtT(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

type adminAgentRow struct {
	MachineID   string `json:"machineId"`
	Name        string `json:"name"`
	KeyPrefix   string `json:"keyPrefix"`
	ConnectedAt string `json:"connectedAt"`
	Phones      int    `json:"phones"`
}

// liveAgentRows snapshots the connected agents. Agent pointers are collected
// under h.mu and their phone counts read under each a.mu AFTERWARDS — never
// nest the two locks.
func (h *Hub) liveAgentRows() []adminAgentRow {
	h.mu.Lock()
	agents := make([]*agent, 0, len(h.agents))
	for _, a := range h.agents {
		agents = append(agents, a)
	}
	h.mu.Unlock()

	rows := make([]adminAgentRow, 0, len(agents))
	for _, a := range agents {
		a.mu.Lock()
		phones := len(a.phones)
		a.mu.Unlock()
		rows = append(rows, adminAgentRow{
			MachineID:   a.id,
			Name:        a.name,
			ConnectedAt: fmtT(a.connectedAt),
			Phones:      phones,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].MachineID < rows[j].MachineID })
	return rows
}

func (h *Hub) adminStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAdminErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rows := h.liveAgentRows()
	if h.Lic != nil {
		if ms, err := h.Lic.ListMachines(); err == nil {
			prefix := make(map[string]string, len(ms))
			for _, m := range ms {
				prefix[m.MachineID] = m.KeyPrefix
			}
			for i := range rows {
				rows[i].KeyPrefix = prefix[rows[i].MachineID]
			}
		}
	}
	phones := 0
	for _, a := range rows {
		phones += a.Phones
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	version := "dev"
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		version = bi.Main.Version
	}
	writeJSON(w, map[string]any{
		"uptime":        int(time.Since(h.started).Seconds()),
		"version":       version,
		"goVersion":     runtime.Version(),
		"memMB":         mem.Alloc / (1 << 20),
		"agentsOnline":  len(rows),
		"phoneSessions": phones,
		"agents":        rows,
	})
}
```

并加 `adminKeys`/`adminKeyRevoke`/`adminKeyExtend`/`adminMachines`/`adminMachineUnbind` 的**临时占位**（Task 4/5 再换成真实现，先让本 task 测试编译通过）：

```go
func (h *Hub) adminKeys(w http.ResponseWriter, r *http.Request) {
	if h.Lic == nil {
		writeAdminErr(w, http.StatusNotFound, "license mode off")
		return
	}
	writeAdminErr(w, http.StatusNotImplemented, "todo")
}

func (h *Hub) adminKeyRevoke(w http.ResponseWriter, r *http.Request)    { h.adminKeys(w, r) }
func (h *Hub) adminKeyExtend(w http.ResponseWriter, r *http.Request)    { h.adminKeys(w, r) }
func (h *Hub) adminMachines(w http.ResponseWriter, r *http.Request)     { h.adminKeys(w, r) }
func (h *Hub) adminMachineUnbind(w http.ResponseWriter, r *http.Request) { h.adminKeys(w, r) }
```

- [ ] **Step 5: 跑测试确认通过**

Run: `cd bridge && go test ./internal/hub/ -run 'TestAdminAuthGate|TestAdminLicenseModeOff' -v`
Expected: 2 个 PASS

- [ ] **Step 6: Commit**

```bash
git add bridge/internal/hub/
git commit -m "feat(hub): admin API 骨架 —— 独立 Handler + Bearer 常数时间认证"
```

### Task 4: 卡密端点（issue/list/revoke/extend）

**Files:**
- Modify: `bridge/internal/hub/admin.go`
- Modify: `bridge/internal/hub/admin_test.go`

- [ ] **Step 1: 写失败测试**

`bridge/internal/hub/admin_test.go` 末尾追加：

```go
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
```

测试文件 import 需补 `"strings"`。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd bridge && go test ./internal/hub/ -run TestAdminKey -v`
Expected: FAIL（占位返回 501）

- [ ] **Step 3: 真实现替换占位**

`bridge/internal/hub/admin.go` 中删除 Task 3 的 5 个占位函数，并在 import 块加入 `"log"`，替换为：

```go
type adminKeyRow struct {
	Prefix    string `json:"prefix"`
	Plan      string `json:"plan"`
	Limit     int    `json:"limit"`
	Used      int    `json:"used"`
	ExpiresAt string `json:"expiresAt"`
	Revoked   bool   `json:"revoked"`
}

// adminKeys: GET = list, POST = issue. The issue response is the plaintext
// key's ONLY appearance — log the prefix, never the key.
func (h *Hub) adminKeys(w http.ResponseWriter, r *http.Request) {
	if h.Lic == nil {
		writeAdminErr(w, http.StatusNotFound, "license mode off")
		return
	}
	switch r.Method {
	case http.MethodGet:
		keys, err := h.Lic.ListKeys()
		if err != nil {
			writeAdminErr(w, licErrStatus(err), err.Error())
			return
		}
		rows := make([]adminKeyRow, 0, len(keys))
		for _, k := range keys {
			rows = append(rows, adminKeyRow{
				Prefix: k.Prefix, Plan: k.Plan, Limit: k.Limit, Used: k.Used,
				ExpiresAt: fmtT(k.ExpiresAt), Revoked: k.Revoked,
			})
		}
		writeJSON(w, rows)
	case http.MethodPost:
		var req struct {
			Plan         string `json:"plan"`
			MachineLimit int    `json:"machineLimit"`
			Months       int    `json:"months"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeAdminErr(w, http.StatusBadRequest, "bad request")
			return
		}
		if req.Plan == "" || req.MachineLimit < 1 || req.Months < 1 {
			writeAdminErr(w, http.StatusBadRequest, "plan/machineLimit/months required")
			return
		}
		key, err := h.Lic.IssueKey(req.Plan, req.MachineLimit, req.Months)
		if err != nil {
			writeAdminErr(w, licErrStatus(err), err.Error())
			return
		}
		log.Printf("hub admin: issued key prefix=%q plan=%q machines=%d months=%d",
			key[:12], req.Plan, req.MachineLimit, req.Months)
		writeJSON(w, map[string]string{"key": key})
	default:
		writeAdminErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Hub) adminKeyRevoke(w http.ResponseWriter, r *http.Request) {
	if h.Lic == nil {
		writeAdminErr(w, http.StatusNotFound, "license mode off")
		return
	}
	if r.Method != http.MethodPost {
		writeAdminErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Prefix string `json:"prefix"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || req.Prefix == "" {
		writeAdminErr(w, http.StatusBadRequest, "prefix required")
		return
	}
	if err := h.Lic.RevokeKeyByPrefix(req.Prefix); err != nil {
		writeAdminErr(w, licErrStatus(err), err.Error())
		return
	}
	log.Printf("hub admin: revoked key prefix=%q", req.Prefix)
	writeJSON(w, map[string]bool{"ok": true})
}

func (h *Hub) adminKeyExtend(w http.ResponseWriter, r *http.Request) {
	if h.Lic == nil {
		writeAdminErr(w, http.StatusNotFound, "license mode off")
		return
	}
	if r.Method != http.MethodPost {
		writeAdminErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Prefix string `json:"prefix"`
		Months int    `json:"months"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil ||
		req.Prefix == "" || req.Months < 1 {
		writeAdminErr(w, http.StatusBadRequest, "prefix/months required")
		return
	}
	if err := h.Lic.ExtendKeyByPrefix(req.Prefix, req.Months); err != nil {
		writeAdminErr(w, licErrStatus(err), err.Error())
		return
	}
	log.Printf("hub admin: extended key prefix=%q months=%d", req.Prefix, req.Months)
	writeJSON(w, map[string]bool{"ok": true})
}
```

（`adminMachines`/`adminMachineUnbind` 两个占位保留到 Task 5：

```go
func (h *Hub) adminMachines(w http.ResponseWriter, r *http.Request)      { h.notImplemented(w) }
func (h *Hub) adminMachineUnbind(w http.ResponseWriter, r *http.Request) { h.notImplemented(w) }

func (h *Hub) notImplemented(w http.ResponseWriter) {
	writeAdminErr(w, http.StatusNotImplemented, "todo")
}
```
）

- [ ] **Step 4: 跑测试确认通过**

Run: `cd bridge && go test ./internal/hub/ -run TestAdmin -v`
Expected: 全部 PASS。注意 `TestAdminLicenseModeOff` 仍须通过。

- [ ] **Step 5: Commit**

```bash
git add bridge/internal/hub/
git commit -m "feat(hub): admin 卡密端点 —— 发/列/吊销/续期，明文仅出现一次"
```

### Task 5: 机器名册 + 解绑 + 状态合并

**Files:**
- Modify: `bridge/internal/hub/admin.go`
- Modify: `bridge/internal/hub/admin_test.go`

- [ ] **Step 1: 写失败测试**

`bridge/internal/hub/admin_test.go` 末尾追加：

```go
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
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd bridge && go test ./internal/hub/ -run 'TestAdminMachines|TestAdminStatus' -v`
Expected: `TestAdminMachinesAndUnbind` FAIL（占位 501）；`TestAdminStatusShape` 应已 PASS（Task 3 实现过）

- [ ] **Step 3: 真实现替换占位**

`bridge/internal/hub/admin.go` 删除 `notImplemented` 与两个占位，替换为：

```go
type adminMachineRow struct {
	MachineID   string `json:"machineId"`
	Name        string `json:"name"`
	KeyPrefix   string `json:"keyPrefix"`
	ActivatedAt string `json:"activatedAt"`
	LastSeen    string `json:"lastSeen"`
	Online      bool   `json:"online"`
	Phones      int    `json:"phones"`
}

// adminMachines merges the persistent roster with the live link table: a row
// is online iff its machineId currently holds an agent registration.
func (h *Hub) adminMachines(w http.ResponseWriter, r *http.Request) {
	if h.Lic == nil {
		writeAdminErr(w, http.StatusNotFound, "license mode off")
		return
	}
	if r.Method != http.MethodGet {
		writeAdminErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ms, err := h.Lic.ListMachines()
	if err != nil {
		writeAdminErr(w, licErrStatus(err), err.Error())
		return
	}
	live := make(map[string]int) // machineId → phone count
	for _, a := range h.liveAgentRows() {
		live[a.MachineID] = a.Phones
	}
	rows := make([]adminMachineRow, 0, len(ms))
	for _, m := range ms {
		phones, online := live[m.MachineID]
		rows = append(rows, adminMachineRow{
			MachineID: m.MachineID, Name: m.Name, KeyPrefix: m.KeyPrefix,
			ActivatedAt: fmtT(m.ActivatedAt), LastSeen: fmtT(m.LastSeen),
			Online: online, Phones: phones,
		})
	}
	writeJSON(w, rows)
}

func (h *Hub) adminMachineUnbind(w http.ResponseWriter, r *http.Request) {
	if h.Lic == nil {
		writeAdminErr(w, http.StatusNotFound, "license mode off")
		return
	}
	if r.Method != http.MethodPost {
		writeAdminErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		MachineID string `json:"machineId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || req.MachineID == "" {
		writeAdminErr(w, http.StatusBadRequest, "machineId required")
		return
	}
	if err := h.Lic.AdminDeactivate(req.MachineID); err != nil {
		writeAdminErr(w, licErrStatus(err), err.Error())
		return
	}
	h.kickAgent(req.MachineID, "unbound by operator")
	log.Printf("hub admin: unbound machine=%q", req.MachineID)
	writeJSON(w, map[string]bool{"ok": true})
}
```

- [ ] **Step 4: 跑全部测试确认通过**

Run: `cd bridge && go test ./internal/hub/ -v -count=1`
Expected: 全部 PASS（含原有 hub/license 模式测试）

- [ ] **Step 5: Commit**

```bash
git add bridge/internal/hub/
git commit -m "feat(hub): admin 机器名册/解绑/状态 —— 名册⨝实时连接表"
```

### Task 6: main.go 接线 + 包注释 + 全量验证

**Files:**
- Modify: `bridge/cmd/codexhub/main.go`

- [ ] **Step 1: 接线 admin 监听**

`bridge/cmd/codexhub/main.go` 中 `srv := &http.Server{...}` 之前插入：

```go
	if adminAddr := os.Getenv("HUB_ADMIN_ADDR"); adminAddr != "" {
		adminKey := os.Getenv("HUB_ADMIN_KEY")
		if adminKey == "" {
			fmt.Fprintln(os.Stderr, "FATAL: HUB_ADMIN_ADDR is set but HUB_ADMIN_KEY is empty")
			os.Exit(1)
		}
		asrv := &http.Server{
			Addr:              adminAddr,
			Handler:           h.AdminHandler(adminKey),
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			fmt.Printf("codex-hub admin api on %s (keep this loopback-only)\n", adminAddr)
			if err := asrv.ListenAndServe(); err != nil {
				fmt.Fprintln(os.Stderr, "hub admin:", err)
				os.Exit(1)
			}
		}()
	}
```

- [ ] **Step 2: 更新包注释**

`main.go` 顶部包注释的 env 列表（`HUB_DB` 行后）追加：

```go
//	HUB_ADMIN_ADDR         operator admin API listen address (e.g. 127.0.0.1:8768).
//	                       Empty (default) = admin API off. NEVER expose this
//	                       listener publicly — nginx must not proxy it; the RuoYi
//	                       admin backend on the same host is the only caller.
//	HUB_ADMIN_KEY          Bearer secret for the admin API (required when
//	                       HUB_ADMIN_ADDR is set)
```

- [ ] **Step 3: 全量验证**

Run: `cd bridge && go vet ./... && go build ./cmd/... && go test ./... -count=1`
Expected: vet/构建无错，测试全 PASS

- [ ] **Step 4: 冒烟验证（手动）**

```bash
cd bridge
HUB_AGENT_KEY=k HUB_LICENSE_MODE=1 HUB_DB=/tmp/smoke-hub.db \
  HUB_ADMIN_ADDR=127.0.0.1:8768 HUB_ADMIN_KEY=smokekey \
  go run ./cmd/codexhub &
sleep 1
curl -s -X POST -H 'Authorization: Bearer smokekey' \
  -d '{"plan":"beta","machineLimit":1,"months":1}' http://127.0.0.1:8768/admin/keys
curl -s -H 'Authorization: Bearer smokekey' http://127.0.0.1:8768/admin/keys
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8768/admin/keys   # 无密钥
kill %1; rm -f /tmp/smoke-hub.db*
```

Expected: 第一条返回 `{"key":"crk_..."}`；第二条返回单行数组；第三条输出 `401`。

- [ ] **Step 5: Commit**

```bash
git add bridge/cmd/codexhub/main.go
git commit -m "feat(codexhub): HUB_ADMIN_ADDR/HUB_ADMIN_KEY 接线 admin API"
```

## Phase 3 — Java：RuoYi 薄代理（admin/ruoyi-ext）

> 本机不要求 JVM 工具链：Java 代码的编译验证发生在 Task 14（服务器上 `mvn package`）。

### Task 7: Java 模块四个类 + 配置样例 + README

**Files:**
- Create: `admin/ruoyi-ext/src/main/java/com/ruoyi/caret/HubAdminClient.java`
- Create: `admin/ruoyi-ext/src/main/java/com/ruoyi/caret/CaretKeyController.java`
- Create: `admin/ruoyi-ext/src/main/java/com/ruoyi/caret/CaretMachineController.java`
- Create: `admin/ruoyi-ext/src/main/java/com/ruoyi/caret/CaretStatusController.java`
- Create: `admin/ruoyi-ext/application-caret.yml`
- Create: `admin/ruoyi-ext/README.md`

- [ ] **Step 1: HubAdminClient.java**

```java
package com.ruoyi.caret;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;

import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Component;

import com.alibaba.fastjson2.JSONObject;
import com.ruoyi.common.exception.ServiceException;

/**
 * codexhub 内部 admin API 的瘦客户端。hub 只在回环地址监听，凭 Bearer 共享密钥访问。
 * 本类无状态、纯转发；明文卡密只存在于 issue 响应中，绝不打日志。
 */
@Component
public class HubAdminClient {

    @Value("${caret.hub.url}")
    private String baseUrl;

    @Value("${caret.hub.key}")
    private String adminKey;

    public String get(String path) {
        return call("GET", path, null);
    }

    public String post(String path, String jsonBody) {
        return call("POST", path, jsonBody);
    }

    private String call(String method, String path, String jsonBody) {
        HttpURLConnection conn = null;
        try {
            conn = (HttpURLConnection) new URL(baseUrl + path).openConnection();
            conn.setRequestMethod(method);
            conn.setConnectTimeout(3000);
            conn.setReadTimeout(10000);
            conn.setRequestProperty("Authorization", "Bearer " + adminKey);
            if (jsonBody != null) {
                conn.setDoOutput(true);
                conn.setRequestProperty("Content-Type", "application/json");
                try (OutputStream os = conn.getOutputStream()) {
                    os.write(jsonBody.getBytes(StandardCharsets.UTF_8));
                }
            }
            int code = conn.getResponseCode();
            String body = readAll(code >= 400 ? conn.getErrorStream() : conn.getInputStream());
            if (code >= 200 && code < 300) {
                return body;
            }
            String msg = "中继返回 " + code;
            try {
                JSONObject err = JSONObject.parseObject(body);
                if (err != null && err.getString("error") != null) {
                    msg = err.getString("error");
                }
            } catch (Exception ignore) {
                // 非 JSON 错误体，保留默认文案
            }
            throw new ServiceException(msg);
        } catch (IOException e) {
            throw new ServiceException("中继服务不可达: " + e.getMessage());
        } finally {
            if (conn != null) {
                conn.disconnect();
            }
        }
    }

    private static String readAll(InputStream in) throws IOException {
        if (in == null) {
            return "";
        }
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        byte[] buf = new byte[4096];
        int n;
        while ((n = in.read(buf)) > 0) {
            out.write(buf, 0, n);
        }
        return out.toString(StandardCharsets.UTF_8.name());
    }
}
```

- [ ] **Step 2: CaretKeyController.java**

```java
package com.ruoyi.caret;

import java.util.Map;

import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

import com.alibaba.fastjson2.JSON;
import com.alibaba.fastjson2.JSONObject;
import com.ruoyi.common.annotation.Log;
import com.ruoyi.common.core.domain.AjaxResult;
import com.ruoyi.common.enums.BusinessType;

/**
 * 卡密管理 —— 全部转发 codexhub admin API。
 * 发卡接口故意不挂 @Log：明文卡密绝不能落进 sys_oper_log。
 */
@RestController
@RequestMapping("/caret/key")
public class CaretKeyController {

    @Autowired
    private HubAdminClient hub;

    @PreAuthorize("@ss.hasPermi('caret:key:list')")
    @GetMapping("/list")
    public AjaxResult list() {
        return AjaxResult.success(JSON.parseArray(hub.get("/admin/keys")));
    }

    @PreAuthorize("@ss.hasPermi('caret:key:issue')")
    @PostMapping("/issue")
    public AjaxResult issue(@RequestBody Map<String, Object> body) {
        JSONObject req = new JSONObject();
        req.put("plan", body.getOrDefault("plan", "beta"));
        req.put("machineLimit", body.getOrDefault("machineLimit", 1));
        req.put("months", body.getOrDefault("months", 1));
        return AjaxResult.success(JSON.parseObject(hub.post("/admin/keys", req.toJSONString())));
    }

    @PreAuthorize("@ss.hasPermi('caret:key:revoke')")
    @Log(title = "卡密-吊销", businessType = BusinessType.UPDATE)
    @PostMapping("/revoke")
    public AjaxResult revoke(@RequestBody Map<String, Object> body) {
        JSONObject req = new JSONObject();
        req.put("prefix", body.get("prefix"));
        hub.post("/admin/keys/revoke", req.toJSONString());
        return AjaxResult.success();
    }

    @PreAuthorize("@ss.hasPermi('caret:key:extend')")
    @Log(title = "卡密-续期", businessType = BusinessType.UPDATE)
    @PostMapping("/extend")
    public AjaxResult extend(@RequestBody Map<String, Object> body) {
        JSONObject req = new JSONObject();
        req.put("prefix", body.get("prefix"));
        req.put("months", body.getOrDefault("months", 1));
        hub.post("/admin/keys/extend", req.toJSONString());
        return AjaxResult.success();
    }
}
```

- [ ] **Step 3: CaretMachineController.java**

```java
package com.ruoyi.caret;

import java.util.Map;

import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

import com.alibaba.fastjson2.JSON;
import com.alibaba.fastjson2.JSONObject;
import com.ruoyi.common.annotation.Log;
import com.ruoyi.common.core.domain.AjaxResult;
import com.ruoyi.common.enums.BusinessType;

/**
 * 机器名册 —— 名册查询与解绑（解绑会同时把在线 agent 踢下线）。
 */
@RestController
@RequestMapping("/caret/machine")
public class CaretMachineController {

    @Autowired
    private HubAdminClient hub;

    @PreAuthorize("@ss.hasPermi('caret:machine:list')")
    @GetMapping("/list")
    public AjaxResult list() {
        return AjaxResult.success(JSON.parseArray(hub.get("/admin/machines")));
    }

    @PreAuthorize("@ss.hasPermi('caret:machine:unbind')")
    @Log(title = "机器-解绑", businessType = BusinessType.DELETE)
    @PostMapping("/unbind")
    public AjaxResult unbind(@RequestBody Map<String, Object> body) {
        JSONObject req = new JSONObject();
        req.put("machineId", body.get("machineId"));
        hub.post("/admin/machines/unbind", req.toJSONString());
        return AjaxResult.success();
    }
}
```

- [ ] **Step 4: CaretStatusController.java**

```java
package com.ruoyi.caret;

import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

import com.alibaba.fastjson2.JSON;
import com.ruoyi.common.core.domain.AjaxResult;

/**
 * 中继状态 —— hub 实时连接表与运行指标（前端 10s 轮询）。
 */
@RestController
@RequestMapping("/caret/status")
public class CaretStatusController {

    @Autowired
    private HubAdminClient hub;

    @PreAuthorize("@ss.hasPermi('caret:status:view')")
    @GetMapping
    public AjaxResult status() {
        return AjaxResult.success(JSON.parseObject(hub.get("/admin/status")));
    }
}
```

- [ ] **Step 5: application-caret.yml**

```yaml
# Caret 运营模块 —— codexhub admin API 对接。
# 用法：把本文件拷到 ruoyi-admin/src/main/resources/，并在 application.yml 的
# spring.profiles.active 里追加 caret（如 active: druid,caret）。
# key 必须与 hub 服务的 HUB_ADMIN_KEY 完全一致（openssl rand -hex 32 生成）。
caret:
  hub:
    url: http://127.0.0.1:8768
    key: REPLACE_WITH_HUB_ADMIN_KEY
```

- [ ] **Step 6: README.md**

```markdown
# admin/ruoyi-ext — RuoYi 侧 Caret 运营模块（drop-in）

把 codexhub 的内部 admin API 代理进 RuoYi-Vue 后端。无状态：卡密数据
始终在 hub 的 SQLite；MySQL 只存 RuoYi 框架数据。

## 对接版本

- 上游：https://github.com/yangzongzhuan/RuoYi-Vue **tag v3.8.8**（JDK 8，fastjson2）
- 升级上游时确认 `com.ruoyi.common.{annotation.Log, core.domain.AjaxResult,
  enums.BusinessType, exception.ServiceException}` 与 `@ss.hasPermi` 仍在。

## 安装（3 步）

1. 拷源码：`src/main/java/com/ruoyi/caret/` → 上游 `ruoyi-admin/src/main/java/com/ruoyi/caret/`
2. 拷配置：`application-caret.yml` → `ruoyi-admin/src/main/resources/`，
   把 `caret.hub.key` 换成与 hub `HUB_ADMIN_KEY` 相同的值；
   `application.yml` 的 `spring.profiles.active` 追加 `,caret`
3. 建菜单：在 ry 库执行 `sql/caret_menu.sql`（admin 角色拥有全部权限，无需另行授权）

## 安全注意

- 发卡接口（`POST /caret/key/issue`）**没有也不能挂 @Log**：响应含明文卡密，
  不得落 `sys_oper_log`。
- hub 的 admin API 只应监听 127.0.0.1，nginx 不得代理它。
```

- [ ] **Step 7: Commit**

```bash
git add admin/ruoyi-ext/
git commit -m "feat(admin): RuoYi 侧 Caret 薄代理模块（drop-in）"
```

### Task 8: 菜单/权限 SQL

**Files:**
- Create: `admin/ruoyi-ext/sql/caret_menu.sql`

- [ ] **Step 1: 写 SQL**

```sql
-- Caret 运营菜单与按钮权限（RuoYi sys_menu）。在 ry 库执行一次。
-- admin 角色（role_id=1）默认拥有全部权限，无需写 sys_role_menu。

INSERT INTO sys_menu (menu_name, parent_id, order_num, path, component, is_frame, is_cache, menu_type, visible, status, perms, icon, create_by, create_time)
VALUES ('Caret 运营', 0, 5, 'caret', NULL, 1, 0, 'M', '0', '0', '', 'monitor', 'admin', sysdate());
SET @caret_dir := LAST_INSERT_ID();

-- 卡密管理
INSERT INTO sys_menu (menu_name, parent_id, order_num, path, component, is_frame, is_cache, menu_type, visible, status, perms, icon, create_by, create_time)
VALUES ('卡密管理', @caret_dir, 1, 'key', 'caret/key/index', 1, 0, 'C', '0', '0', 'caret:key:list', 'lock', 'admin', sysdate());
SET @m_key := LAST_INSERT_ID();
INSERT INTO sys_menu (menu_name, parent_id, order_num, path, component, is_frame, is_cache, menu_type, visible, status, perms, icon, create_by, create_time) VALUES
('卡密发行', @m_key, 1, '', NULL, 1, 0, 'F', '0', '0', 'caret:key:issue',  '#', 'admin', sysdate()),
('卡密吊销', @m_key, 2, '', NULL, 1, 0, 'F', '0', '0', 'caret:key:revoke', '#', 'admin', sysdate()),
('卡密续期', @m_key, 3, '', NULL, 1, 0, 'F', '0', '0', 'caret:key:extend', '#', 'admin', sysdate());

-- 机器名册
INSERT INTO sys_menu (menu_name, parent_id, order_num, path, component, is_frame, is_cache, menu_type, visible, status, perms, icon, create_by, create_time)
VALUES ('机器名册', @caret_dir, 2, 'machine', 'caret/machine/index', 1, 0, 'C', '0', '0', 'caret:machine:list', 'server', 'admin', sysdate());
SET @m_machine := LAST_INSERT_ID();
INSERT INTO sys_menu (menu_name, parent_id, order_num, path, component, is_frame, is_cache, menu_type, visible, status, perms, icon, create_by, create_time)
VALUES ('机器解绑', @m_machine, 1, '', NULL, 1, 0, 'F', '0', '0', 'caret:machine:unbind', '#', 'admin', sysdate());

-- 中继状态
INSERT INTO sys_menu (menu_name, parent_id, order_num, path, component, is_frame, is_cache, menu_type, visible, status, perms, icon, create_by, create_time)
VALUES ('中继状态', @caret_dir, 3, 'status', 'caret/status/index', 1, 0, 'C', '0', '0', 'caret:status:view', 'online', 'admin', sysdate());
```

- [ ] **Step 2: Commit**

```bash
git add admin/ruoyi-ext/sql/caret_menu.sql
git commit -m "feat(admin): Caret 运营菜单 SQL（sys_menu drop-in）"
```

## Phase 4 — 前端（admin/web）

### Task 9: 模板落库

**Files:**
- Create: `admin/web/`（整个模板）
- Modify: `admin/web/.gitignore`、`admin/web/.env.development`、`admin/web/.env.production`
- Create: `admin/web/.env.example`

- [ ] **Step 1: 解压落库**

```bash
cd ~/codex-remote
mkdir -p admin
unzip -q ~/Downloads/ele-admin-plus-ruoyi-ts-pro-20260525.zip -d /tmp/ele-tpl-drop
mv /tmp/ele-tpl-drop/ele-admin-plus-ruoyi-ts-pro admin/web
rm -rf /tmp/ele-tpl-drop
```

- [ ] **Step 2: 授权码不入库**

`admin/web/.gitignore` 末尾追加一行：

```
.env
```

新建 `admin/web/.env.example`：

```
# EleAdminPlus 授权码（付费模板，不入库）—— 复制本文件为 .env 并填入你的授权码
VITE_LICENSE=
```

（`.env` 留在工作区供本地构建，git 不跟踪。）

- [ ] **Step 3: 环境配置**

`admin/web/.env.development` 改：

```
VITE_APP_NAME=Caret 运营后台
VITE_API_URL=http://localhost:8080
```

（其余行保留。）`admin/web/.env.production` 改：

```
VITE_APP_NAME=Caret 运营后台
VITE_API_URL=/prod-api
```

（其余行保留。）

- [ ] **Step 4: 安装依赖 + 构建烟测**

Run: `cd admin/web && npm install && npm run build`
Expected: 构建成功产出 `dist/`（模板自带授权码可供演示构建；无网络/授权报错则记录并继续——不阻塞后续 task 的代码编写）

- [ ] **Step 5: Commit**

```bash
cd ~/codex-remote
git add admin/web
git status   # 确认 .env 不在暂存区
git commit -m "feat(admin): ele-admin-plus-ruoyi-ts-pro 前端模板落库（授权码不入库）"
```

### Task 10: api/caret 三模块

**Files:**
- Create: `admin/web/src/api/caret/key/index.ts`、`admin/web/src/api/caret/key/types.ts`
- Create: `admin/web/src/api/caret/machine/index.ts`、`admin/web/src/api/caret/machine/types.ts`
- Create: `admin/web/src/api/caret/status/index.ts`、`admin/web/src/api/caret/status/types.ts`

- [ ] **Step 1: key 模块**

`admin/web/src/api/caret/key/types.ts`：

```ts
export interface CaretKey {
  /** 卡密前缀（明文只在发卡时出现一次） */
  prefix: string;
  /** 套餐 */
  plan: string;
  /** 机器数上限 */
  limit: number;
  /** 已绑定机器数 */
  used: number;
  /** 到期时间（RFC3339） */
  expiresAt: string;
  /** 是否已吊销 */
  revoked: boolean;
}

export interface IssueKeyForm {
  plan: string;
  machineLimit: number;
  months: number;
}
```

`admin/web/src/api/caret/key/index.ts`（沿用模板 api 模块惯例）：

```ts
import request from '@/utils/request';
import type { ApiResult } from '@/api/types';
import type { CaretKey, IssueKeyForm } from './types';

/**
 * 卡密列表
 */
export async function listKeys() {
  const res = await request.get<ApiResult<CaretKey[]>>('/caret/key/list');
  if (res.data.code === 200) {
    return res.data.data ?? [];
  }
  return Promise.reject(new Error(res.data.msg));
}

/**
 * 发卡（返回的明文卡密只出现这一次）
 */
export async function issueKey(data: IssueKeyForm) {
  const res = await request.post<ApiResult<{ key: string }>>('/caret/key/issue', data);
  if (res.data.code === 200) {
    return res.data.data.key;
  }
  return Promise.reject(new Error(res.data.msg));
}

/**
 * 吊销
 */
export async function revokeKey(prefix: string) {
  const res = await request.post<ApiResult>('/caret/key/revoke', { prefix });
  if (res.data.code === 200) {
    return res.data.msg;
  }
  return Promise.reject(new Error(res.data.msg));
}

/**
 * 续期
 */
export async function extendKey(prefix: string, months: number) {
  const res = await request.post<ApiResult>('/caret/key/extend', { prefix, months });
  if (res.data.code === 200) {
    return res.data.msg;
  }
  return Promise.reject(new Error(res.data.msg));
}
```

- [ ] **Step 2: machine 模块**

`admin/web/src/api/caret/machine/types.ts`：

```ts
export interface CaretMachine {
  machineId: string;
  name: string;
  /** 所属卡密前缀 */
  keyPrefix: string;
  /** 激活时间（RFC3339） */
  activatedAt: string;
  /** 最后在线（RFC3339，空串=从未上线） */
  lastSeen: string;
  online: boolean;
  /** 当前手机会话数 */
  phones: number;
}
```

`admin/web/src/api/caret/machine/index.ts`：

```ts
import request from '@/utils/request';
import type { ApiResult } from '@/api/types';
import type { CaretMachine } from './types';

/**
 * 机器名册（名册 ⨝ 实时在线状态）
 */
export async function listMachines() {
  const res = await request.get<ApiResult<CaretMachine[]>>('/caret/machine/list');
  if (res.data.code === 200) {
    return res.data.data ?? [];
  }
  return Promise.reject(new Error(res.data.msg));
}

/**
 * 解绑（同时把在线 agent 踢下线）
 */
export async function unbindMachine(machineId: string) {
  const res = await request.post<ApiResult>('/caret/machine/unbind', { machineId });
  if (res.data.code === 200) {
    return res.data.msg;
  }
  return Promise.reject(new Error(res.data.msg));
}
```

- [ ] **Step 3: status 模块**

`admin/web/src/api/caret/status/types.ts`：

```ts
export interface CaretAgent {
  machineId: string;
  name: string;
  keyPrefix: string;
  /** 上线时间（RFC3339） */
  connectedAt: string;
  /** 手机会话数 */
  phones: number;
}

export interface CaretStatus {
  /** hub 运行秒数 */
  uptime: number;
  version: string;
  goVersion: string;
  memMB: number;
  agentsOnline: number;
  phoneSessions: number;
  agents: CaretAgent[];
}
```

`admin/web/src/api/caret/status/index.ts`：

```ts
import request from '@/utils/request';
import type { ApiResult } from '@/api/types';
import type { CaretStatus } from './types';

/**
 * 中继实时状态
 */
export async function getStatus() {
  const res = await request.get<ApiResult<CaretStatus>>('/caret/status');
  if (res.data.code === 200) {
    return res.data.data;
  }
  return Promise.reject(new Error(res.data.msg));
}
```

- [ ] **Step 4: 类型检查**

Run: `cd admin/web && npx vue-tsc --noEmit`
Expected: 无错误

- [ ] **Step 5: Commit**

```bash
git add admin/web/src/api/caret
git commit -m "feat(admin): caret 前端 api 模块（key/machine/status）"
```

### Task 11: 卡密管理页

**Files:**
- Create: `admin/web/src/views/caret/key/index.vue`

- [ ] **Step 1: 页面实现**

```vue
<template>
  <ele-page hide-footer>
    <ele-card bordered>
      <div style="margin-bottom: 12px">
        <el-button type="primary" @click="openIssue">生成卡密</el-button>
        <el-button @click="load">刷新</el-button>
      </div>
      <el-table v-loading="loading" :data="keys" row-key="prefix">
        <el-table-column prop="prefix" label="卡密前缀" min-width="160">
          <template #default="{ row }">
            <span style="font-family: monospace">{{ row.prefix }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="plan" label="套餐" width="100" align="center" />
        <el-table-column label="机器" width="90" align="center">
          <template #default="{ row }">{{ row.used }} / {{ row.limit }}</template>
        </el-table-column>
        <el-table-column label="到期时间" width="120" align="center">
          <template #default="{ row }">{{ row.expiresAt.slice(0, 10) }}</template>
        </el-table-column>
        <el-table-column label="状态" width="90" align="center">
          <template #default="{ row }">
            <el-tag v-if="row.revoked" type="danger">已吊销</el-tag>
            <el-tag v-else-if="expired(row)" type="warning">已过期</el-tag>
            <el-tag v-else type="success">正常</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="150" align="center">
          <template #default="{ row }">
            <el-button link type="primary" @click="handleExtend(row)">续期</el-button>
            <el-button link type="danger" :disabled="row.revoked" @click="handleRevoke(row)">
              吊销
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </ele-card>

    <!-- 发卡弹窗：成功后一次性展示明文 -->
    <el-dialog v-model="issueVisible" title="生成卡密" width="420px" :close-on-click-modal="false">
      <template v-if="!issuedKey">
        <el-form :model="issueForm" label-width="80px">
          <el-form-item label="套餐">
            <el-input v-model="issueForm.plan" placeholder="beta" />
          </el-form-item>
          <el-form-item label="机器数">
            <el-input-number v-model="issueForm.machineLimit" :min="1" :max="10" />
          </el-form-item>
          <el-form-item label="月数">
            <el-input-number v-model="issueForm.months" :min="1" :max="36" />
          </el-form-item>
        </el-form>
      </template>
      <template v-else>
        <el-alert type="warning" :closable="false" show-icon title="卡密只显示这一次，关闭后无法再查看，请立即复制" />
        <el-input :model-value="issuedKey" readonly style="margin-top: 12px; font-family: monospace" />
      </template>
      <template #footer>
        <template v-if="!issuedKey">
          <el-button @click="issueVisible = false">取消</el-button>
          <el-button type="primary" :loading="issuing" @click="doIssue">生成</el-button>
        </template>
        <template v-else>
          <el-button type="primary" @click="copyKey">复制卡密</el-button>
          <el-button @click="closeIssue">完成</el-button>
        </template>
      </template>
    </el-dialog>
  </ele-page>
</template>

<script lang="ts" setup>
  import { ref, reactive } from 'vue';
  import { ElMessageBox } from 'element-plus';
  import { EleMessage } from 'ele-admin-plus';
  import { listKeys, issueKey, revokeKey, extendKey } from '@/api/caret/key';
  import type { CaretKey, IssueKeyForm } from '@/api/caret/key/types';

  defineOptions({ name: 'CaretKey' });

  const loading = ref(false);
  const keys = ref<CaretKey[]>([]);

  const issueVisible = ref(false);
  const issuing = ref(false);
  const issuedKey = ref('');
  const issueForm = reactive<IssueKeyForm>({ plan: 'beta', machineLimit: 1, months: 6 });

  const expired = (row: CaretKey) => new Date(row.expiresAt).getTime() < Date.now();

  const load = () => {
    loading.value = true;
    listKeys()
      .then((data) => {
        keys.value = data;
      })
      .catch((e) => EleMessage.error({ message: e.message, plain: true }))
      .finally(() => {
        loading.value = false;
      });
  };

  const openIssue = () => {
    issuedKey.value = '';
    issueVisible.value = true;
  };

  const doIssue = () => {
    issuing.value = true;
    issueKey({ ...issueForm })
      .then((key) => {
        issuedKey.value = key;
        load();
      })
      .catch((e) => EleMessage.error({ message: e.message, plain: true }))
      .finally(() => {
        issuing.value = false;
      });
  };

  const copyKey = () => {
    navigator.clipboard
      .writeText(issuedKey.value)
      .then(() => EleMessage.success({ message: '已复制', plain: true }))
      .catch(() => EleMessage.error({ message: '复制失败，请手动选中复制', plain: true }));
  };

  const closeIssue = () => {
    issueVisible.value = false;
    issuedKey.value = '';
  };

  const handleRevoke = (row: CaretKey) => {
    ElMessageBox.confirm(`确认吊销卡密 ${row.prefix}…？其机器将在下次校验时被拒绝。`, '系统提示', {
      type: 'warning',
      draggable: true
    })
      .then(() => revokeKey(row.prefix))
      .then(() => {
        EleMessage.success({ message: '已吊销', plain: true });
        load();
      })
      .catch((e) => {
        if (e instanceof Error) {
          EleMessage.error({ message: e.message, plain: true });
        }
      });
  };

  const handleExtend = (row: CaretKey) => {
    ElMessageBox.prompt('续期月数', `续期 ${row.prefix}…`, {
      inputValue: '6',
      inputPattern: /^[1-9]\d*$/,
      inputErrorMessage: '请输入正整数',
      draggable: true
    })
      .then(({ value }) => extendKey(row.prefix, Number(value)))
      .then(() => {
        EleMessage.success({ message: '已续期', plain: true });
        load();
      })
      .catch((e) => {
        if (e instanceof Error) {
          EleMessage.error({ message: e.message, plain: true });
        }
      });
  };

  load();
</script>
```

- [ ] **Step 2: 类型检查**

Run: `cd admin/web && npx vue-tsc --noEmit`
Expected: 无错误

- [ ] **Step 3: Commit**

```bash
git add admin/web/src/views/caret/key
git commit -m "feat(admin): 卡密管理页 —— 发卡一次性明文/吊销/续期"
```

### Task 12: 机器名册页

**Files:**
- Create: `admin/web/src/views/caret/machine/index.vue`

- [ ] **Step 1: 页面实现**

```vue
<template>
  <ele-page hide-footer>
    <ele-card bordered>
      <div style="margin-bottom: 12px">
        <el-button @click="load">刷新</el-button>
      </div>
      <el-table v-loading="loading" :data="machines" row-key="machineId">
        <el-table-column prop="name" label="机器名" min-width="140" />
        <el-table-column prop="machineId" label="机器 ID" min-width="180">
          <template #default="{ row }">
            <span style="font-family: monospace">{{ row.machineId }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="keyPrefix" label="所属卡密" width="140">
          <template #default="{ row }">
            <span style="font-family: monospace">{{ row.keyPrefix }}</span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="90" align="center">
          <template #default="{ row }">
            <el-tag v-if="row.online" type="success">在线</el-tag>
            <el-tag v-else type="info">离线</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="phones" label="手机会话" width="90" align="center" />
        <el-table-column label="激活时间" width="120" align="center">
          <template #default="{ row }">{{ row.activatedAt.slice(0, 10) }}</template>
        </el-table-column>
        <el-table-column label="最后在线" width="170" align="center">
          <template #default="{ row }">
            {{ row.lastSeen ? row.lastSeen.replace('T', ' ').slice(0, 16) : '从未上线' }}
          </template>
        </el-table-column>
        <el-table-column label="操作" width="90" align="center">
          <template #default="{ row }">
            <el-button link type="danger" @click="handleUnbind(row)">解绑</el-button>
          </template>
        </el-table-column>
      </el-table>
    </ele-card>
  </ele-page>
</template>

<script lang="ts" setup>
  import { ref } from 'vue';
  import { ElMessageBox } from 'element-plus';
  import { EleMessage } from 'ele-admin-plus';
  import { listMachines, unbindMachine } from '@/api/caret/machine';
  import type { CaretMachine } from '@/api/caret/machine/types';

  defineOptions({ name: 'CaretMachine' });

  const loading = ref(false);
  const machines = ref<CaretMachine[]>([]);

  const load = () => {
    loading.value = true;
    listMachines()
      .then((data) => {
        machines.value = data;
      })
      .catch((e) => EleMessage.error({ message: e.message, plain: true }))
      .finally(() => {
        loading.value = false;
      });
  };

  const handleUnbind = (row: CaretMachine) => {
    ElMessageBox.confirm(
      `确认解绑「${row.name || row.machineId}」？将释放卡密槽位并把它踢下线。`,
      '系统提示',
      { type: 'warning', draggable: true }
    )
      .then(() => unbindMachine(row.machineId))
      .then(() => {
        EleMessage.success({ message: '已解绑', plain: true });
        load();
      })
      .catch((e) => {
        if (e instanceof Error) {
          EleMessage.error({ message: e.message, plain: true });
        }
      });
  };

  load();
</script>
```

- [ ] **Step 2: 类型检查**

Run: `cd admin/web && npx vue-tsc --noEmit`
Expected: 无错误

- [ ] **Step 3: Commit**

```bash
git add admin/web/src/views/caret/machine
git commit -m "feat(admin): 机器名册页 —— 在线状态 + 解绑"
```

### Task 13: 中继状态页 + 前端整体验证

**Files:**
- Create: `admin/web/src/views/caret/status/index.vue`

- [ ] **Step 1: 页面实现**

```vue
<template>
  <ele-page hide-footer>
    <el-alert
      v-if="offline"
      type="error"
      :closable="false"
      show-icon
      title="中继不可达 —— 显示的是最后一次成功获取的数据"
      style="margin-bottom: 12px"
    />
    <el-row :gutter="12">
      <el-col v-for="card in cards" :key="card.label" :xs="12" :sm="8" :md="4">
        <ele-card bordered>
          <div style="font-size: 12px; color: var(--el-text-color-secondary)">
            {{ card.label }}
          </div>
          <div style="font-size: 22px; font-weight: 600; margin-top: 4px">{{ card.value }}</div>
        </ele-card>
      </el-col>
    </el-row>
    <ele-card bordered header="在线机器" style="margin-top: 12px">
      <el-table :data="status?.agents ?? []" row-key="machineId">
        <el-table-column prop="name" label="机器名" min-width="140" />
        <el-table-column prop="machineId" label="机器 ID" min-width="180">
          <template #default="{ row }">
            <span style="font-family: monospace">{{ row.machineId }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="keyPrefix" label="所属卡密" width="140">
          <template #default="{ row }">
            <span style="font-family: monospace">{{ row.keyPrefix }}</span>
          </template>
        </el-table-column>
        <el-table-column label="在线时长" width="120" align="center">
          <template #default="{ row }">{{ since(row.connectedAt) }}</template>
        </el-table-column>
        <el-table-column prop="phones" label="手机会话" width="90" align="center" />
      </el-table>
    </ele-card>
  </ele-page>
</template>

<script lang="ts" setup>
  import { ref, computed, onUnmounted } from 'vue';
  import { getStatus } from '@/api/caret/status';
  import type { CaretStatus } from '@/api/caret/status/types';

  defineOptions({ name: 'CaretStatus' });

  const status = ref<CaretStatus | null>(null);
  const offline = ref(false);

  const fmtDuration = (sec: number) => {
    if (sec < 3600) {
      return `${Math.floor(sec / 60)} 分钟`;
    }
    if (sec < 86400) {
      return `${Math.floor(sec / 3600)} 小时 ${Math.floor((sec % 3600) / 60)} 分`;
    }
    return `${Math.floor(sec / 86400)} 天 ${Math.floor((sec % 86400) / 3600)} 小时`;
  };

  const since = (iso: string) => {
    if (!iso) {
      return '-';
    }
    const sec = Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 1000));
    return fmtDuration(sec);
  };

  const cards = computed(() => [
    { label: '在线机器', value: status.value?.agentsOnline ?? '-' },
    { label: '手机会话', value: status.value?.phoneSessions ?? '-' },
    { label: '运行时长', value: status.value ? fmtDuration(status.value.uptime) : '-' },
    { label: '内存 (MB)', value: status.value?.memMB ?? '-' },
    { label: '版本', value: status.value?.version ?? '-' },
    { label: 'Go', value: status.value?.goVersion ?? '-' }
  ]);

  const load = () => {
    getStatus()
      .then((data) => {
        status.value = data;
        offline.value = false;
      })
      .catch(() => {
        offline.value = true; // 轮询失败只亮横幅，不弹窗刷屏
      });
  };

  load();
  const timer = setInterval(load, 10_000);
  onUnmounted(() => clearInterval(timer));
</script>
```

- [ ] **Step 2: 整体验证**

Run: `cd admin/web && npx vue-tsc --noEmit && npm run build`
Expected: 类型检查无错，构建成功

- [ ] **Step 3: Commit**

```bash
git add admin/web/src/views/caret/status
git commit -m "feat(admin): 中继状态仪表盘 —— 10s 轮询 + 离线横幅"
```

## Phase 5 — 部署（香港中继，宝塔）

> 操作型 runbook。`<ADMIN_DOMAIN>` 在开工前定一个子域名（占位 `admin.example.com`）并在宝塔解析/建站。

### Task 14: 服务器 RuoYi 全家桶

- [ ] **Step 1: 装运行时**（宝塔软件商店或 shell）

```bash
# MySQL 5.7+ / Redis：宝塔软件商店安装，均只监听 127.0.0.1
yum install -y java-1.8.0-openjdk-devel maven git
```

- [ ] **Step 2: 拉取 RuoYi 并 drop-in 模块**

```bash
git clone --depth 1 -b v3.8.8 https://github.com/yangzongzhuan/RuoYi-Vue.git /opt/ruoyi-src
# 把本仓库 admin/ruoyi-ext 上传到服务器 /opt/ruoyi-ext 后：
cp -r /opt/ruoyi-ext/src/main/java/com/ruoyi/caret \
      /opt/ruoyi-src/ruoyi-admin/src/main/java/com/ruoyi/
cp /opt/ruoyi-ext/application-caret.yml \
   /opt/ruoyi-src/ruoyi-admin/src/main/resources/
```

编辑 `/opt/ruoyi-src/ruoyi-admin/src/main/resources/application.yml`：`spring.profiles.active` 改为 `druid,caret`；
编辑 `application-caret.yml`：`caret.hub.key` 填 Task 15 生成的 `HUB_ADMIN_KEY`。

- [ ] **Step 3: 初始化数据库**

```bash
mysql -uroot -p -e "CREATE DATABASE ry DEFAULT CHARACTER SET utf8mb4;"
# --default-character-set=utf8mb4 必须带上：脚本含中文（菜单名/种子数据），
# 客户端默认 latin1 时会静默双重编码成乱码。
mysql --default-character-set=utf8mb4 -uroot -p ry < /opt/ruoyi-src/sql/ry_*.sql
mysql --default-character-set=utf8mb4 -uroot -p ry < /opt/ruoyi-src/sql/quartz.sql
mysql --default-character-set=utf8mb4 -uroot -p ry < /opt/ruoyi-ext/sql/caret_menu.sql
```

编辑 `application-druid.yml` 数据库账号密码、`application.yml` redis 密码。

- [ ] **Step 4: 构建 + systemd**

```bash
cd /opt/ruoyi-src && mvn -q package -DskipTests
install -D ruoyi-admin/target/ruoyi-admin.jar /opt/ruoyi/ruoyi-admin.jar
cat > /etc/systemd/system/ruoyi.service <<'EOF'
[Unit]
Description=RuoYi admin backend (Caret console)
After=network.target mysqld.service redis.service

[Service]
ExecStart=/usr/bin/java -jar /opt/ruoyi/ruoyi-admin.jar
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload && systemctl enable --now ruoyi
```

Run: `curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/captchaImage`
Expected: `200`

### Task 15: hub 升级 + nginx + 前端发布 + 加固

- [ ] **Step 1: hub 二进制升级 + admin 环境变量**

```bash
# 本机交叉编译并上传
cd bridge && GOOS=linux GOARCH=amd64 go build -o /tmp/codexhub ./cmd/codexhub
scp /tmp/codexhub root@<HUB_HOST>:/usr/local/bin/codexhub.new
# 服务器上：
openssl rand -hex 32   # 记为 HUB_ADMIN_KEY，同时填进 application-caret.yml
# 在 codexhub 的 systemd unit / 环境文件中追加：
#   HUB_ADMIN_ADDR=127.0.0.1:8768
#   HUB_ADMIN_KEY=<上面生成的值>
mv /usr/local/bin/codexhub.new /usr/local/bin/codexhub && systemctl restart codexhub
journalctl -u codexhub -n 5   # 应见 "admin api on 127.0.0.1:8768"
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8768/admin/status   # 401 = 在听且要密钥
```

- [ ] **Step 2: 前端构建发布**

```bash
cd admin/web && npm run build
scp -r dist/* root@<HUB_HOST>:/www/wwwroot/<ADMIN_DOMAIN>/
```

- [ ] **Step 3: 宝塔建站 + nginx**

宝塔新建静态站点 `<ADMIN_DOMAIN>`（root `/www/wwwroot/<ADMIN_DOMAIN>`）+ Let's Encrypt 证书，配置文件加：

```nginx
location /prod-api/ {
    proxy_pass http://127.0.0.1:8080/;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
}
location / {
    try_files $uri $uri/ /index.html;
}
location ^~ /prod-api/druid/ { return 403; }
```

- [ ] **Step 4: 加固**

- 浏览器打开 `https://<ADMIN_DOMAIN>`，用 admin/admin123 登录后**立即改密码**（系统管理 → 用户管理）。
- 确认验证码开启（默认开）；确认 MySQL/Redis 无公网端口（`ss -tlnp` 检查）。
- 确认 hub admin API 公网不可达：`curl https://<ADMIN_DOMAIN>/admin/status` 与 `curl http://<HUB_HOST>:8768/` 均失败。

### Task 16: 端到端验收

- [ ] 登录后台，左侧出现「Caret 运营」目录与三个页面
- [ ] 卡密管理：生成一张卡（beta/1 台/1 月）→ 明文弹出 → 复制 → 关闭后列表多一行 used=0
- [ ] 真机激活：在一台桌面机 `codexbridge activate` 用该卡 → 机器名册出现该机、状态在线
- [ ] 中继状态：仪表盘显示在线机器 1、手机 App 连上后会话数 +1
- [ ] 解绑该机 → agent 被踢下线（bridge 日志可见）、名册行消失、卡密 used 归 0
- [ ] 吊销该卡 → 状态列「已吊销」；CLI `codexhub admin list-keys` 交叉可见同样状态
- [ ] 续期另一张卡 → 到期日推后；CLI 交叉确认
- [ ] RuoYi 操作日志：吊销/续期/解绑有记录，且任何日志中**搜不到 `crk_` 全文卡密**
