# License 与机器绑定（阶段一内测）实现计划 — Plan 1/2：Go 控制面

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 codexhub 上落地 License Key + 机器名册控制面：激活签发机器凭证、agent 注册校验、自助解绑限频、到期宽限、admin CLI 人工发 key。

**Architecture:** 新增 `bridge/internal/license` 包（SQLite 持久化，modernc.org/sqlite 纯 Go 驱动），hub 通过 `Hub.Lic` 字段接入（nil = 现有 self-host 模式，行为零变化）。bridge 端新增 `codexbridge activate` 子命令与机器凭证注册。安全钳制（server.go）一概不动。

**Tech Stack:** Go 1.26、modernc.org/sqlite（纯 Go，免 CGO 交叉编译）、database/sql、coder/websocket（已有）。

**Scope split:** 本计划只覆盖 Go 侧（license 包、hub 集成、admin CLI、bridge 激活）。完成后系统自身完整可用：发 key→激活→注册→解绑（经 hub HTTP API / admin CLI）。**Plan 2（另写）**：Flutter App 与小程序的机器名册 UI，消费本计划的 `/api/roster` 接口。

**Spec:** `docs/superpowers/specs/2026-06-10-subscription-machine-binding-design.md`

**约定提醒（执行者必读）：**
- 每个 Go 包的顶部包注释是协议规范，改行为必须同步改注释（Task 14 统一收口，但新文件的包注释随文件写）。
- 测试跑法：`cd bridge && go test ./internal/license/ -v`（包级）；全量 `go test ./...`。
- 时间相关测试通过注入 `Service.Now` 控制，禁止 sleep。
- 秘密（key/凭证）只存 SHA-256 hex，比较用 `crypto/subtle`。

---

## 文件结构总览

```
bridge/internal/license/          # 新包：订阅控制面（hub 持有）
  license.go      包注释 + Service + 错误 + 常量
  store.go        Open/migrate/Close（SQLite schema）
  keys.go         NewKey/ValidKeyFormat/NewCredential/hashSecret
  admin.go        IssueKey/ListKeys/RevokeKey/ExtendKey
  activate.go     Activate（占槽/指纹复用/满员/重名）
  roster.go       Roster/AccountIDForMachine/Deactivate/AdminDeactivate
  check.go        CheckAgent/TouchLastSeen（注册时 + 重验）
  *_test.go       各文件对应测试
bridge/internal/hub/
  hub.go          [改] frame.Cred、Hub.Lic、agent.cred、authAgent、notice
  api.go          [新] /api/activate、/api/roster、/api/roster/deactivate、kickAgent
  recheck.go      [新] RunRecheck 周期重验
  license_test.go [新] license 模式集成测试
bridge/internal/provision/
  fingerprint.go          [新] FingerprintHint（共享）
  fingerprint_darwin.go   [新] platformUUID (ioreg IOPlatformUUID)
  fingerprint_windows.go  [新] platformUUID (registry MachineGuid)
  fingerprint_other.go    [新] platformUUID → ""
bridge/internal/bridge/
  agent.go        [改] hubFrame.Cred、AgentConfig.Cred、register 带 cred、notice 日志
bridge/cmd/codexhub/
  main.go         [改] HUB_LICENSE_MODE/HUB_DB 装配 + admin 分发
  admin.go        [新] issue-key/list-keys/revoke-key/extend-key/unbind
bridge/cmd/codexbridge/
  main.go         [改] agent 子命令 -cred/-cred-file；activate 子命令分发
  activate.go     [新] runActivate（POST /api/activate）
```

---

### Task 1: license 包骨架 — 依赖、类型、SQLite schema

**Files:**
- Modify: `bridge/go.mod`（go get）
- Create: `bridge/internal/license/license.go`
- Create: `bridge/internal/license/store.go`
- Test: `bridge/internal/license/store_test.go`

- [ ] **Step 1: 拉依赖**

```bash
cd bridge && go get modernc.org/sqlite@latest
```

- [ ] **Step 2: 写失败测试** `store_test.go`

```go
package license

import (
	"path/filepath"
	"testing"
)

// tempSvc opens a Service on a throwaway db; shared by every test in the package.
func tempSvc(t *testing.T) *Service {
	t.Helper()
	svc, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func TestOpenMigrateIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.db")
	svc, err := Open(p)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	svc2, err := Open(p) // re-open re-runs migrate — must be a no-op, not an error
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	_ = svc2.Close()
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `cd bridge && go test ./internal/license/ -v`
Expected: FAIL（编译错误：`Open`/`Service` 未定义）

- [ ] **Step 4: 写 `license.go`**

```go
// Package license is the hub's subscription control plane (phase 1: invite-only
// beta — keys are issued by the operator's admin CLI, no payments). It owns the
// SQLite-backed account/key/subscription/machine roster and answers three
// questions for the hub:
//
//   - Activate: may this license key bind a (new) machine, and with what
//     per-machine credential?
//   - CheckAgent: may this machine, presenting its credential, register right
//     now? (also re-asked hourly for long-lived links)
//   - Deactivate: free a roster slot — self-service, rate-limited to
//     SwapLimit/SwapWindow so a key can't be shared by rotating machines.
//
// Secrets discipline: license keys and machine credentials are stored as
// SHA-256 hex ONLY; the plaintext is shown once at issue/activate time and
// never again. Hash comparison goes through crypto/subtle.
//
// Fingerprint (fp_hint) is an opportunistic hardware hint, never a hard gate:
// a reinstall with a matching hint silently reuses its old slot (no swap
// burned); a mismatch just takes the normal slot path.
//
// Design doc: docs/superpowers/specs/2026-06-10-subscription-machine-binding-design.md
package license

import (
	"database/sql"
	"errors"
	"time"
)

var (
	ErrBadKey        = errors.New("license: invalid key")
	ErrRevoked       = errors.New("license: key revoked")
	ErrExpired       = errors.New("license: subscription expired")
	ErrRosterFull    = errors.New("license: machine limit reached")
	ErrMachineTaken  = errors.New("license: machine id already in use")
	ErrBadMachineID  = errors.New("license: bad machine id")
	ErrSwapLimit     = errors.New("license: swap limit reached")
	ErrUnknownMachine = errors.New("license: unknown machine")
	ErrBadCredential = errors.New("license: bad machine credential")
)

const (
	// GracePeriod keeps an expired subscription's agents connectable while the
	// user renews; after it the hub refuses register (roster rows are kept).
	GracePeriod = 72 * time.Hour
	// SwapWindow/SwapLimit bound self-service deactivations — the anti-sharing
	// control (fingerprint reuse does NOT count against it).
	SwapWindow = 30 * 24 * time.Hour
	SwapLimit  = 3
)

// Service is the SQLite-backed control plane. Safe for concurrent use.
type Service struct {
	db *sql.DB
	// Now is injectable for expiry/grace/swap-window tests; defaults to time.Now.
	Now func() time.Time
}
```

- [ ] **Step 5: 写 `store.go`**

```go
package license

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: no CGO, cross-compiles for the hub host
)

// Open opens (creating if needed) the license db and applies the schema.
// WAL + busy_timeout make cross-process access safe (admin CLI alongside hub).
func Open(path string) (*Service, error) {
	db, err := sql.Open("sqlite",
		"file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Service{db: db, Now: time.Now}, nil
}

func (s *Service) Close() error { return s.db.Close() }

// migrate applies the idempotent schema. Phase-1 simplifications: one
// subscription per account (UNIQUE), times are unix seconds UTC, machines are
// soft-deleted (deactivated_at) for audit.
func migrate(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS accounts (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	email      TEXT UNIQUE,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS license_keys (
	key_hash   TEXT PRIMARY KEY,
	prefix     TEXT NOT NULL,
	account_id INTEGER NOT NULL REFERENCES accounts(id),
	created_at INTEGER NOT NULL,
	revoked    INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS subscriptions (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id    INTEGER NOT NULL UNIQUE REFERENCES accounts(id),
	plan          TEXT NOT NULL,
	machine_limit INTEGER NOT NULL,
	status        TEXT NOT NULL DEFAULT 'active',
	expires_at    INTEGER NOT NULL,
	source        TEXT NOT NULL,
	external_ref  TEXT
);
CREATE TABLE IF NOT EXISTS machines (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id     INTEGER NOT NULL REFERENCES accounts(id),
	machine_id     TEXT NOT NULL,
	name           TEXT NOT NULL DEFAULT '',
	fp_hint        TEXT NOT NULL DEFAULT '',
	cred_hash      TEXT NOT NULL,
	activated_at   INTEGER NOT NULL,
	last_seen      INTEGER,
	deactivated_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_machines_active_id
	ON machines(machine_id) WHERE deactivated_at IS NULL;
CREATE TABLE IF NOT EXISTS swap_events (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id INTEGER NOT NULL REFERENCES accounts(id),
	ts         INTEGER NOT NULL
);`)
	return err
}
```

- [ ] **Step 6: 跑测试确认通过**

Run: `cd bridge && go test ./internal/license/ -v`
Expected: PASS `TestOpenMigrateIdempotent`

- [ ] **Step 7: Commit**

```bash
cd bridge && git add go.mod go.sum internal/license/
git commit -m "feat(license): SQLite-backed control-plane skeleton (schema + Open/migrate)"
```

---

### Task 2: key 与凭证生成（keys.go）

**Files:**
- Create: `bridge/internal/license/keys.go`
- Test: `bridge/internal/license/keys_test.go`

- [ ] **Step 1: 写失败测试** `keys_test.go`

```go
package license

import (
	"strings"
	"testing"
)

func TestNewKeyFormat(t *testing.T) {
	k, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	if !strings.HasPrefix(k, "crk_") || len(k) != 34 {
		t.Fatalf("bad shape: %q (len %d)", k, len(k))
	}
	if !ValidKeyFormat(k) {
		t.Fatalf("fresh key fails its own checksum: %q", k)
	}
	k2, _ := NewKey()
	if k == k2 {
		t.Fatal("two keys identical")
	}
}

func TestValidKeyFormatCatchesTypo(t *testing.T) {
	k, _ := NewKey()
	b := []byte(k)
	if b[10] == 'a' {
		b[10] = 'b'
	} else {
		b[10] = 'a'
	}
	if ValidKeyFormat(string(b)) {
		t.Fatal("single-char typo passed the checksum")
	}
	if ValidKeyFormat("crk_short") || ValidKeyFormat("") {
		t.Fatal("malformed keys accepted")
	}
}

func TestNewCredentialShape(t *testing.T) {
	c, err := NewCredential()
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	if !strings.HasPrefix(c, "mc_") || len(c) != 3+64 {
		t.Fatalf("bad shape: %q (len %d)", c, len(c))
	}
	c2, _ := NewCredential()
	if c == c2 {
		t.Fatal("two credentials identical")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd bridge && go test ./internal/license/ -run 'TestNewKey|TestValidKey|TestNewCredential' -v`
Expected: FAIL（`NewKey` 未定义）

- [ ] **Step 3: 写 `keys.go`**

```go
package license

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"strings"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewKey mints a license key: "crk_" + 26 base32 chars (16 random bytes) + 4
// checksum chars. The checksum catches typos client-side before any network
// round-trip; authorization is always the server-side hash lookup.
func NewKey() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	body := strings.ToLower(b32.EncodeToString(raw))
	return "crk_" + body + keyChecksum(body), nil
}

// ValidKeyFormat reports whether k is structurally a crk_ key with a good
// checksum. Typo detection ONLY — never an authorization check.
func ValidKeyFormat(k string) bool {
	if !strings.HasPrefix(k, "crk_") || len(k) != 34 {
		return false
	}
	body, sum := k[4:30], k[30:]
	return keyChecksum(body) == sum
}

func keyChecksum(body string) string {
	sum := sha256.Sum256([]byte("crk:" + body))
	return strings.ToLower(b32.EncodeToString(sum[:]))[:4]
}

// NewCredential mints a per-machine credential ("mc_" + 64 hex chars), issued
// at activation, presented in every agent register, stored hashed.
func NewCredential() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "mc_" + hex.EncodeToString(raw), nil
}

// hashSecret is the storage form of every key/credential: SHA-256 hex.
func hashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd bridge && go test ./internal/license/ -v`
Expected: PASS（全部）

- [ ] **Step 5: Commit**

```bash
cd bridge && git add internal/license/keys.go internal/license/keys_test.go
git commit -m "feat(license): key + machine-credential generation with typo checksum"
```

---

### Task 3: admin 操作 — IssueKey/ListKeys/RevokeKey/ExtendKey（admin.go）

**Files:**
- Create: `bridge/internal/license/admin.go`
- Test: `bridge/internal/license/admin_test.go`

- [ ] **Step 1: 写失败测试** `admin_test.go`

```go
package license

import (
	"errors"
	"testing"
	"time"
)

func TestIssueListRevokeExtend(t *testing.T) {
	svc := tempSvc(t)
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return base }

	key, err := svc.IssueKey("beta", 3, 6)
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}
	if !ValidKeyFormat(key) {
		t.Fatalf("issued key malformed: %q", key)
	}

	infos, err := svc.ListKeys()
	if err != nil || len(infos) != 1 {
		t.Fatalf("ListKeys: %v (n=%d)", err, len(infos))
	}
	ki := infos[0]
	if ki.Plan != "beta" || ki.Limit != 3 || ki.Used != 0 || ki.Revoked {
		t.Fatalf("bad info: %+v", ki)
	}
	if ki.Prefix != key[:12] {
		t.Fatalf("prefix %q, want %q", ki.Prefix, key[:12])
	}
	if want := base.AddDate(0, 6, 0); !ki.ExpiresAt.Equal(want) {
		t.Fatalf("expires %v, want %v", ki.ExpiresAt, want)
	}

	if err := svc.ExtendKey(key, 6); err != nil {
		t.Fatalf("ExtendKey: %v", err)
	}
	infos, _ = svc.ListKeys()
	if want := base.AddDate(0, 12, 0); !infos[0].ExpiresAt.Equal(want) {
		t.Fatalf("after extend: %v, want %v", infos[0].ExpiresAt, want)
	}

	if err := svc.RevokeKey(key); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}
	infos, _ = svc.ListKeys()
	if !infos[0].Revoked {
		t.Fatal("not revoked")
	}

	ghost, _ := NewKey() // valid format, never issued
	if err := svc.RevokeKey(ghost); !errors.Is(err, ErrBadKey) {
		t.Fatalf("revoke unknown: %v, want ErrBadKey", err)
	}
	if err := svc.ExtendKey("garbage", 1); !errors.Is(err, ErrBadKey) {
		t.Fatalf("extend garbage: %v, want ErrBadKey", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd bridge && go test ./internal/license/ -run TestIssueListRevokeExtend -v`
Expected: FAIL（`IssueKey` 未定义）

- [ ] **Step 3: 写 `admin.go`**

```go
package license

import (
	"database/sql"
	"errors"
	"time"
)

// KeyInfo is one row of ListKeys — operator-facing, never contains the key
// itself (only its display prefix; the full key exists only at issue time).
type KeyInfo struct {
	Prefix    string
	Plan      string
	Limit     int
	Used      int
	ExpiresAt time.Time
	Revoked   bool
}

// IssueKey creates account + subscription + key in one shot (the phase-1
// "manual" path — a future payment webhook calls this same method) and returns
// the plaintext key ONCE. Only its hash is stored.
func (s *Service) IssueKey(plan string, machineLimit, months int) (string, error) {
	key, err := NewKey()
	if err != nil {
		return "", err
	}
	now := s.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.Exec(`INSERT INTO accounts(created_at) VALUES(?)`, now.Unix())
	if err != nil {
		return "", err
	}
	aid, err := res.LastInsertId()
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(
		`INSERT INTO license_keys(key_hash, prefix, account_id, created_at) VALUES(?,?,?,?)`,
		hashSecret(key), key[:12], aid, now.Unix()); err != nil {
		return "", err
	}
	if _, err := tx.Exec(
		`INSERT INTO subscriptions(account_id, plan, machine_limit, status, expires_at, source)
		 VALUES(?,?,?,'active',?,'manual')`,
		aid, plan, machineLimit, now.AddDate(0, months, 0).Unix()); err != nil {
		return "", err
	}
	return key, tx.Commit()
}

// ListKeys returns every key with its subscription and live machine count.
func (s *Service) ListKeys() ([]KeyInfo, error) {
	rows, err := s.db.Query(`
		SELECT k.prefix, s.plan, s.machine_limit, s.expires_at, k.revoked,
		       (SELECT COUNT(*) FROM machines m
		         WHERE m.account_id = k.account_id AND m.deactivated_at IS NULL)
		FROM license_keys k
		JOIN subscriptions s ON s.account_id = k.account_id
		ORDER BY k.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KeyInfo
	for rows.Next() {
		var ki KeyInfo
		var exp int64
		var revoked int
		if err := rows.Scan(&ki.Prefix, &ki.Plan, &ki.Limit, &exp, &revoked, &ki.Used); err != nil {
			return nil, err
		}
		ki.ExpiresAt = time.Unix(exp, 0).UTC()
		ki.Revoked = revoked != 0
		out = append(out, ki)
	}
	return out, rows.Err()
}

// RevokeKey bars the key (and, via CheckAgent's live-key check, its machines
// at the next recheck). The roster is kept — un-revoke is a manual db edit in
// phase 1.
func (s *Service) RevokeKey(key string) error {
	res, err := s.db.Exec(`UPDATE license_keys SET revoked=1 WHERE key_hash=?`, hashSecret(key))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrBadKey
	}
	return nil
}

// ExtendKey pushes expiry out by months from max(now, current expiry) and
// re-activates the subscription status.
func (s *Service) ExtendKey(key string, months int) error {
	sub, err := s.accountByKeyAnyState(key)
	if err != nil {
		return err
	}
	base := sub.ExpiresAt
	if now := s.Now().UTC(); now.After(base) {
		base = now
	}
	_, err = s.db.Exec(
		`UPDATE subscriptions SET expires_at=?, status='active' WHERE account_id=?`,
		base.AddDate(0, months, 0).Unix(), sub.AccountID)
	return err
}

// subRow is the joined key→account→subscription view used by lookups.
type subRow struct {
	AccountID    int64
	Plan         string
	MachineLimit int
	Status       string
	ExpiresAt    time.Time
	Revoked      bool
}

// accountByKeyAnyState resolves a key regardless of revocation (admin paths).
func (s *Service) accountByKeyAnyState(key string) (subRow, error) {
	var r subRow
	var exp int64
	var revoked int
	err := s.db.QueryRow(`
		SELECT k.account_id, k.revoked, s.plan, s.machine_limit, s.status, s.expires_at
		FROM license_keys k JOIN subscriptions s ON s.account_id = k.account_id
		WHERE k.key_hash = ?`, hashSecret(key)).
		Scan(&r.AccountID, &revoked, &r.Plan, &r.MachineLimit, &r.Status, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return subRow{}, ErrBadKey
	}
	if err != nil {
		return subRow{}, err
	}
	r.ExpiresAt = time.Unix(exp, 0).UTC()
	r.Revoked = revoked != 0
	return r, nil
}

// accountByKey is the user-facing variant: revoked keys are refused.
func (s *Service) accountByKey(key string) (subRow, error) {
	r, err := s.accountByKeyAnyState(key)
	if err != nil {
		return subRow{}, err
	}
	if r.Revoked {
		return subRow{}, ErrRevoked
	}
	return r, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd bridge && go test ./internal/license/ -v`
Expected: PASS（全部）

- [ ] **Step 5: Commit**

```bash
cd bridge && git add internal/license/admin.go internal/license/admin_test.go
git commit -m "feat(license): issue/list/revoke/extend keys (manual phase-1 path)"
```

---

### Task 4: 激活 — Activate（activate.go）

**Files:**
- Create: `bridge/internal/license/activate.go`
- Test: `bridge/internal/license/activate_test.go`

- [ ] **Step 1: 写失败测试** `activate_test.go`

```go
package license

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var testBase = time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)

// issuedSvc returns a Service pinned to testBase with one key of the given limit.
func issuedSvc(t *testing.T, limit int) (*Service, string) {
	t.Helper()
	svc := tempSvc(t)
	svc.Now = func() time.Time { return testBase }
	key, err := svc.IssueKey("beta", limit, 6)
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}
	return svc, key
}

func TestActivateHappyPath(t *testing.T) {
	svc, key := issuedSvc(t, 2)
	res, err := svc.Activate(key, "m1", "Office Mac", "fp1")
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if !strings.HasPrefix(res.Credential, "mc_") || res.Used != 1 || res.Limit != 2 || res.Reused {
		t.Fatalf("bad result: %+v", res)
	}
}

func TestActivateRosterFull(t *testing.T) {
	svc, key := issuedSvc(t, 1)
	if _, err := svc.Activate(key, "m1", "", "fp1"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := svc.Activate(key, "m2", "", "fp2"); !errors.Is(err, ErrRosterFull) {
		t.Fatalf("second: %v, want ErrRosterFull", err)
	}
}

func TestActivateReusesSlotOnFingerprintMatch(t *testing.T) {
	svc, key := issuedSvc(t, 1)
	first, err := svc.Activate(key, "m1", "Mac", "fp1")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	// reinstall: same fingerprint, new machine id — reuse the slot, reissue cred
	again, err := svc.Activate(key, "m1-fresh", "Mac", "fp1")
	if err != nil {
		t.Fatalf("reuse: %v", err)
	}
	if !again.Reused || again.Used != 1 {
		t.Fatalf("want reuse of the single slot: %+v", again)
	}
	if again.Credential == first.Credential {
		t.Fatal("credential not reissued")
	}
	// old id is gone, old credential dead; new pair works
	if _, err := svc.CheckAgent("m1", first.Credential); !errors.Is(err, ErrUnknownMachine) {
		t.Fatalf("old id: %v, want ErrUnknownMachine", err)
	}
	if _, err := svc.CheckAgent("m1-fresh", first.Credential); !errors.Is(err, ErrBadCredential) {
		t.Fatalf("old cred: %v, want ErrBadCredential", err)
	}
	if _, err := svc.CheckAgent("m1-fresh", again.Credential); err != nil {
		t.Fatalf("new cred: %v", err)
	}
}

func TestActivateMachineIDTakenAcrossAccounts(t *testing.T) {
	svc, key1 := issuedSvc(t, 2)
	key2, err := svc.IssueKey("beta", 2, 6)
	if err != nil {
		t.Fatalf("second key: %v", err)
	}
	if _, err := svc.Activate(key1, "dup", "", "fpA"); err != nil {
		t.Fatalf("first: %v", err)
	}
	// the hub routes by machine id globally, so the second account can't take it
	if _, err := svc.Activate(key2, "dup", "", "fpB"); !errors.Is(err, ErrMachineTaken) {
		t.Fatalf("clash: %v, want ErrMachineTaken", err)
	}
}

func TestActivateRejectsBadExpiredRevoked(t *testing.T) {
	svc, key := issuedSvc(t, 1)
	if _, err := svc.Activate("not-a-key", "m1", "", ""); !errors.Is(err, ErrBadKey) {
		t.Fatalf("garbage: %v, want ErrBadKey", err)
	}
	if _, err := svc.Activate(key, "bad id with spaces", "", ""); !errors.Is(err, ErrBadMachineID) {
		t.Fatalf("bad id: %v, want ErrBadMachineID", err)
	}
	svc.Now = func() time.Time { return testBase.AddDate(0, 7, 0) } // past expiry
	if _, err := svc.Activate(key, "m1", "", ""); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v, want ErrExpired", err)
	}
	svc.Now = func() time.Time { return testBase }
	if err := svc.RevokeKey(key); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := svc.Activate(key, "m1", "", ""); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked: %v, want ErrRevoked", err)
	}
}
```

注意：本测试用到 Task 6 的 `CheckAgent`。先写 `activate.go` 后，`TestActivateReusesSlotOnFingerprintMatch` 仍编译失败 —— 这是预期的跨 Task 依赖：**Task 4 Step 4 先只跑其余测试**（`-run 'TestActivateHappy|TestActivateRoster|TestActivateMachineID|TestActivateRejects'`），Task 6 完成后全量绿。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd bridge && go test ./internal/license/ -run TestActivate -v`
Expected: FAIL（`Activate` 未定义）

- [ ] **Step 3: 写 `activate.go`**

```go
package license

import (
	"regexp"
)

// machineIDRe mirrors what the hub/phone treat as a machine id (it lands in
// URLs and QR payloads): short, no spaces, no exotic characters.
var machineIDRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// ActivateResult carries the one-time plaintext credential plus roster usage
// for the "bound N/limit" UX line.
type ActivateResult struct {
	Credential string
	MachineID  string
	Used       int
	Limit      int
	Reused     bool
}

// Activate binds a machine to the key's account: reuse the slot when the
// fingerprint matches an active machine of the SAME account (reinstall — no
// slot burned, no swap counted), otherwise take a free slot. The per-machine
// credential is (re)issued either way; activation requires an ACTIVE
// subscription (grace is for staying online, not for binding new machines).
func (s *Service) Activate(key, machineID, name, fpHint string) (ActivateResult, error) {
	if !ValidKeyFormat(key) {
		return ActivateResult{}, ErrBadKey
	}
	if !machineIDRe.MatchString(machineID) {
		return ActivateResult{}, ErrBadMachineID
	}
	sub, err := s.accountByKey(key)
	if err != nil {
		return ActivateResult{}, err
	}
	now := s.Now().UTC()
	if sub.Status != "active" || now.After(sub.ExpiresAt) {
		return ActivateResult{}, ErrExpired
	}
	cred, err := NewCredential()
	if err != nil {
		return ActivateResult{}, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return ActivateResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	// Global id uniqueness: the hub routes by machine id across ALL accounts.
	// Exclude the row we may be about to reuse (same account + same fp).
	var clash int
	if fpHint != "" {
		err = tx.QueryRow(`SELECT COUNT(*) FROM machines
			WHERE machine_id=? AND deactivated_at IS NULL
			  AND NOT (account_id=? AND fp_hint=?)`,
			machineID, sub.AccountID, fpHint).Scan(&clash)
	} else {
		err = tx.QueryRow(`SELECT COUNT(*) FROM machines
			WHERE machine_id=? AND deactivated_at IS NULL`, machineID).Scan(&clash)
	}
	if err != nil {
		return ActivateResult{}, err
	}
	if clash > 0 {
		return ActivateResult{}, ErrMachineTaken
	}

	// Reinstall path: an active machine of this account with the same
	// fingerprint gets refreshed in place (id/name may change too).
	if fpHint != "" {
		res, err := tx.Exec(`UPDATE machines
			SET machine_id=?, name=?, cred_hash=?, activated_at=?
			WHERE account_id=? AND fp_hint=? AND deactivated_at IS NULL`,
			machineID, name, hashSecret(cred), now.Unix(), sub.AccountID, fpHint)
		if err != nil {
			return ActivateResult{}, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			used, err := countActive(tx, sub.AccountID)
			if err != nil {
				return ActivateResult{}, err
			}
			if err := tx.Commit(); err != nil {
				return ActivateResult{}, err
			}
			return ActivateResult{Credential: cred, MachineID: machineID,
				Used: used, Limit: sub.MachineLimit, Reused: true}, nil
		}
	}

	used, err := countActive(tx, sub.AccountID)
	if err != nil {
		return ActivateResult{}, err
	}
	if used >= sub.MachineLimit {
		return ActivateResult{}, ErrRosterFull
	}
	if _, err := tx.Exec(`INSERT INTO machines
		(account_id, machine_id, name, fp_hint, cred_hash, activated_at)
		VALUES(?,?,?,?,?,?)`,
		sub.AccountID, machineID, name, fpHint, hashSecret(cred), now.Unix()); err != nil {
		return ActivateResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ActivateResult{}, err
	}
	return ActivateResult{Credential: cred, MachineID: machineID,
		Used: used + 1, Limit: sub.MachineLimit, Reused: false}, nil
}

// rowQuerier lets countActive run inside or outside a transaction.
type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func countActive(q rowQuerier, accountID int64) (int, error) {
	var n int
	err := q.QueryRow(`SELECT COUNT(*) FROM machines
		WHERE account_id=? AND deactivated_at IS NULL`, accountID).Scan(&n)
	return n, err
}
```

（`database/sql` 需补进 import。）

- [ ] **Step 4: 跑除指纹复用外的测试确认通过**

Run: `cd bridge && go test ./internal/license/ -run 'TestActivateHappy|TestActivateRoster|TestActivateMachineID|TestActivateRejects' -v`
Expected: PASS（`TestActivateReusesSlotOnFingerprintMatch` 等 Task 6 的 `CheckAgent` 落地后再全量验证；在此之前包级编译需要它，可临时用 `t.Skip("needs CheckAgent (Task 6)")` 占位首行，Task 6 删除）

- [ ] **Step 5: Commit**

```bash
cd bridge && git add internal/license/activate.go internal/license/activate_test.go
git commit -m "feat(license): activation — slot take, fingerprint reuse, global id uniqueness"
```

---

### Task 5: 名册与换机限频 — Roster/Deactivate（roster.go）

**Files:**
- Create: `bridge/internal/license/roster.go`
- Test: `bridge/internal/license/roster_test.go`

- [ ] **Step 1: 写失败测试** `roster_test.go`

```go
package license

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestDeactivateFreesSlotAndRateLimits(t *testing.T) {
	svc, key := issuedSvc(t, 1)
	now := testBase
	svc.Now = func() time.Time { return now }

	var aid int64
	// 3 swaps inside the window are fine; the 4th hits the limit
	for i := 1; i <= 3; i++ {
		mid := fmt.Sprintf("m%d", i)
		if _, err := svc.Activate(key, mid, "", fmt.Sprintf("fp%d", i)); err != nil {
			t.Fatalf("activate %s: %v", mid, err)
		}
		var err error
		if aid, err = svc.AccountIDForMachine(mid); err != nil {
			t.Fatalf("account for %s: %v", mid, err)
		}
		if err := svc.Deactivate(aid, mid); err != nil {
			t.Fatalf("deactivate %s: %v", mid, err)
		}
	}
	if _, err := svc.Activate(key, "m4", "", "fp4"); err != nil {
		t.Fatalf("activate m4: %v", err)
	}
	if err := svc.Deactivate(aid, "m4"); !errors.Is(err, ErrSwapLimit) {
		t.Fatalf("4th swap: %v, want ErrSwapLimit", err)
	}
	// window slides: 31 days later the swap goes through
	now = testBase.Add(31 * 24 * time.Hour)
	if err := svc.Deactivate(aid, "m4"); err != nil {
		t.Fatalf("after window: %v", err)
	}
	// deactivating a machine that isn't active any more
	if err := svc.Deactivate(aid, "m4"); !errors.Is(err, ErrUnknownMachine) {
		t.Fatalf("double deactivate: %v, want ErrUnknownMachine", err)
	}
}

func TestRosterListsActiveOnly(t *testing.T) {
	svc, key := issuedSvc(t, 2)
	if _, err := svc.Activate(key, "m1", "Mac", "fp1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Activate(key, "m2", "PC", "fp2"); err != nil {
		t.Fatal(err)
	}
	aid, err := svc.AccountIDForMachine("m1")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Deactivate(aid, "m2"); err != nil {
		t.Fatal(err)
	}
	ms, err := svc.Roster(aid)
	if err != nil || len(ms) != 1 || ms[0].MachineID != "m1" || ms[0].Name != "Mac" {
		t.Fatalf("roster: %v %+v", err, ms)
	}
}

func TestAdminDeactivateSkipsSwapLimit(t *testing.T) {
	svc, key := issuedSvc(t, 1)
	aid := int64(0)
	for i := 1; i <= 3; i++ {
		mid := fmt.Sprintf("m%d", i)
		if _, err := svc.Activate(key, mid, "", fmt.Sprintf("fp%d", i)); err != nil {
			t.Fatal(err)
		}
		aid, _ = svc.AccountIDForMachine(mid)
		if err := svc.Deactivate(aid, mid); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Activate(key, "m4", "", "fp4"); err != nil {
		t.Fatal(err)
	}
	// operator override is not rate-limited and burns no swap
	if err := svc.AdminDeactivate("m4"); err != nil {
		t.Fatalf("AdminDeactivate: %v", err)
	}
	if err := svc.AdminDeactivate("m4"); !errors.Is(err, ErrUnknownMachine) {
		t.Fatalf("double: %v, want ErrUnknownMachine", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd bridge && go test ./internal/license/ -run 'TestDeactivate|TestRoster|TestAdminDeactivate' -v`
Expected: FAIL（`Deactivate`/`Roster`/`AccountIDForMachine` 未定义）

- [ ] **Step 3: 写 `roster.go`**

```go
package license

import (
	"database/sql"
	"errors"
	"time"
)

// MachineInfo is one active roster entry (deactivated rows stay in the db for
// audit but never surface here).
type MachineInfo struct {
	MachineID   string
	Name        string
	ActivatedAt time.Time
	LastSeen    time.Time // zero when the machine never registered
}

// AccountIDForMachine maps an ACTIVE machine id to its account — the roster
// API's auth pivot (phone proves it can reach machine X; X's account is what
// it may manage).
func (s *Service) AccountIDForMachine(machineID string) (int64, error) {
	var aid int64
	err := s.db.QueryRow(`SELECT account_id FROM machines
		WHERE machine_id=? AND deactivated_at IS NULL`, machineID).Scan(&aid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrUnknownMachine
	}
	return aid, err
}

// Roster lists the account's active machines, oldest first.
func (s *Service) Roster(accountID int64) ([]MachineInfo, error) {
	rows, err := s.db.Query(`SELECT machine_id, name, activated_at, COALESCE(last_seen, 0)
		FROM machines WHERE account_id=? AND deactivated_at IS NULL
		ORDER BY activated_at`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MachineInfo
	for rows.Next() {
		var m MachineInfo
		var act, seen int64
		if err := rows.Scan(&m.MachineID, &m.Name, &act, &seen); err != nil {
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

// Deactivate frees a roster slot (self-service). Rate-limited to SwapLimit per
// SwapWindow per account — the anti-key-sharing control; fingerprint-reuse
// reinstalls never pass through here so they don't burn swaps.
func (s *Service) Deactivate(accountID int64, machineID string) error {
	now := s.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var swaps int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM swap_events WHERE account_id=? AND ts>?`,
		accountID, now.Add(-SwapWindow).Unix()).Scan(&swaps); err != nil {
		return err
	}
	if swaps >= SwapLimit {
		return ErrSwapLimit
	}
	res, err := tx.Exec(`UPDATE machines SET deactivated_at=?
		WHERE account_id=? AND machine_id=? AND deactivated_at IS NULL`,
		now.Unix(), accountID, machineID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUnknownMachine
	}
	if _, err := tx.Exec(`INSERT INTO swap_events(account_id, ts) VALUES(?,?)`,
		accountID, now.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// AdminDeactivate is the operator override (lost machine, support case): frees
// the slot with no rate limit and no swap burned.
func (s *Service) AdminDeactivate(machineID string) error {
	res, err := s.db.Exec(`UPDATE machines SET deactivated_at=?
		WHERE machine_id=? AND deactivated_at IS NULL`,
		s.Now().UTC().Unix(), machineID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUnknownMachine
	}
	return nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd bridge && go test ./internal/license/ -run 'TestDeactivate|TestRoster|TestAdminDeactivate' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd bridge && git add internal/license/roster.go internal/license/roster_test.go
git commit -m "feat(license): roster queries + rate-limited self-service deactivation"
```

---

### Task 6: 注册校验与重验 — CheckAgent（check.go）

**Files:**
- Create: `bridge/internal/license/check.go`
- Test: `bridge/internal/license/check_test.go`
- Modify: `bridge/internal/license/activate_test.go`（删除 Task 4 的 `t.Skip` 占位）

- [ ] **Step 1: 写失败测试** `check_test.go`

```go
package license

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCheckAgentLifecycle(t *testing.T) {
	svc, key := issuedSvc(t, 1) // expires testBase + 6 months
	now := testBase
	svc.Now = func() time.Time { return now }
	res, err := svc.Activate(key, "m1", "Mac", "fp1")
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	cred := res.Credential

	// active
	cr, err := svc.CheckAgent("m1", cred)
	if err != nil || cr.State != StateActive || cr.Message != "" {
		t.Fatalf("active: %v %+v", err, cr)
	}
	// wrong credential / unknown machine
	if _, err := svc.CheckAgent("m1", "mc_wrong"); !errors.Is(err, ErrBadCredential) {
		t.Fatalf("bad cred: %v", err)
	}
	if _, err := svc.CheckAgent("ghost", cred); !errors.Is(err, ErrUnknownMachine) {
		t.Fatalf("ghost: %v", err)
	}
	// grace: expired but inside GracePeriod — allowed, with a renewal notice
	now = testBase.AddDate(0, 6, 0).Add(time.Hour)
	cr, err = svc.CheckAgent("m1", cred)
	if err != nil || cr.State != StateGrace || !strings.Contains(cr.Message, "expired") {
		t.Fatalf("grace: %v %+v", err, cr)
	}
	// past grace — refused
	now = testBase.AddDate(0, 6, 0).Add(GracePeriod + time.Hour)
	if _, err := svc.CheckAgent("m1", cred); !errors.Is(err, ErrExpired) {
		t.Fatalf("past grace: %v", err)
	}
	// renewal restores access
	now = testBase
	if err := svc.ExtendKey(key, 12); err != nil {
		t.Fatalf("extend: %v", err)
	}
	now = testBase.AddDate(0, 7, 0)
	if cr, err = svc.CheckAgent("m1", cred); err != nil || cr.State != StateActive {
		t.Fatalf("after renew: %v %+v", err, cr)
	}
	// revocation kills it regardless of expiry
	if err := svc.RevokeKey(key); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := svc.CheckAgent("m1", cred); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked: %v", err)
	}
}

func TestTouchLastSeen(t *testing.T) {
	svc, key := issuedSvc(t, 1)
	if _, err := svc.Activate(key, "m1", "", "fp1"); err != nil {
		t.Fatal(err)
	}
	svc.TouchLastSeen("m1")
	aid, _ := svc.AccountIDForMachine("m1")
	ms, err := svc.Roster(aid)
	if err != nil || len(ms) != 1 || ms[0].LastSeen.IsZero() {
		t.Fatalf("last_seen not set: %v %+v", err, ms)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd bridge && go test ./internal/license/ -run 'TestCheckAgent|TestTouchLastSeen' -v`
Expected: FAIL（`CheckAgent`/`StateActive` 未定义）

- [ ] **Step 3: 写 `check.go`**

```go
package license

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AgentState classifies a successful check; hard failures are errors instead.
type AgentState int

const (
	StateActive AgentState = iota
	StateGrace             // expired but inside GracePeriod — allowed + notice
)

// CheckResult is a successful CheckAgent verdict.
type CheckResult struct {
	State     AgentState
	AccountID int64
	Message   string // non-empty in grace: human renewal notice for tray/phone
}

// CheckAgent answers "may this machine register (or stay registered)?". Order
// matters for what we may reveal: credential is verified FIRST, so expiry /
// revocation details only ever reach the machine's legitimate owner.
func (s *Service) CheckAgent(machineID, credential string) (CheckResult, error) {
	var credHash string
	var aid int64
	err := s.db.QueryRow(`SELECT cred_hash, account_id FROM machines
		WHERE machine_id=? AND deactivated_at IS NULL`, machineID).Scan(&credHash, &aid)
	if errors.Is(err, sql.ErrNoRows) {
		return CheckResult{}, ErrUnknownMachine
	}
	if err != nil {
		return CheckResult{}, err
	}
	if subtle.ConstantTimeCompare([]byte(hashSecret(credential)), []byte(credHash)) != 1 {
		return CheckResult{}, ErrBadCredential
	}
	var live int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM license_keys
		WHERE account_id=? AND revoked=0`, aid).Scan(&live); err != nil {
		return CheckResult{}, err
	}
	if live == 0 {
		return CheckResult{}, ErrRevoked
	}
	var status string
	var expUnix int64
	if err := s.db.QueryRow(`SELECT status, expires_at FROM subscriptions
		WHERE account_id=?`, aid).Scan(&status, &expUnix); err != nil {
		return CheckResult{}, err
	}
	if status != "active" {
		return CheckResult{}, ErrExpired
	}
	now := s.Now().UTC()
	exp := time.Unix(expUnix, 0).UTC()
	switch {
	case now.Before(exp):
		return CheckResult{State: StateActive, AccountID: aid}, nil
	case now.Before(exp.Add(GracePeriod)):
		return CheckResult{State: StateGrace, AccountID: aid,
			Message: fmt.Sprintf("subscription expired %s — relay access ends %s, renew to keep remote access",
				exp.Format("2006-01-02"), exp.Add(GracePeriod).Format("2006-01-02 15:04 UTC"))}, nil
	default:
		return CheckResult{}, ErrExpired
	}
}

// TouchLastSeen records liveness (best-effort; errors are deliberately dropped
// — a failed stamp must never break a register).
func (s *Service) TouchLastSeen(machineID string) {
	_, _ = s.db.Exec(`UPDATE machines SET last_seen=?
		WHERE machine_id=? AND deactivated_at IS NULL`,
		s.Now().UTC().Unix(), machineID)
}
```

- [ ] **Step 4: 删除 Task 4 占位 Skip，全包测试**

删掉 `activate_test.go` 中 `TestActivateReusesSlotOnFingerprintMatch` 首行的 `t.Skip(...)`（若 Task 4 加了）。

Run: `cd bridge && go test ./internal/license/ -v`
Expected: PASS（整包全绿）

- [ ] **Step 5: Commit**

```bash
cd bridge && git add internal/license/
git commit -m "feat(license): register-time check with expiry grace + revocation"
```

---

### Task 7: hub 注册门 — license 模式校验机器凭证

**Files:**
- Modify: `bridge/internal/hub/hub.go`
- Test: `bridge/internal/hub/license_test.go`（新建）

- [ ] **Step 1: 写失败测试** `license_test.go`

```go
package hub

import (
	"context"
	"path/filepath"
	"net/http/httptest"
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
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd bridge && go test ./internal/hub/ -run TestLicenseRegister -v`
Expected: FAIL（`frame` 无 `Cred` 字段、`Hub` 无 `Lic` 字段）

- [ ] **Step 3: 改 `hub.go` — frame.Cred、Hub.Lic、agent.cred、authAgent、notice**

3a. `frame` 结构体（`AgentKey` 行后加一行）：

```go
	AgentKey  string `json:"agentKey,omitempty"`
	Cred      string `json:"cred,omitempty"` // license mode: per-machine credential (replaces AgentKey)
```

3b. `Hub` 结构体加字段（`RequireDeviceAuth` 注释块之后）：

```go
	// Lic, when non-nil, switches agent registration from the shared agentKey to
	// per-machine credentials checked against the license roster (HUB_LICENSE_MODE).
	// nil = self-host mode: behavior identical to before this field existed.
	Lic *license.Service
```

import 加 `"github.com/yunyuchen/codex-remote/bridge/internal/license"`。

3c. `agent` 结构体加字段（`token string` 后）：

```go
	cred  string // license mode: the credential this link registered with (for recheck)
```

3d. `handleAgent` 中注册校验改为（原 `if reg.T != "register" || ... !ctEq(...)` 一段）：

```go
	notice, ok := h.authAgent(reg)
	if reg.T != "register" || reg.MachineID == "" || reg.Token == "" || !ok {
		_ = wsjson.Write(ctx, ws, frame{T: "error", Message: "register rejected"})
		_ = ws.Close(websocket.StatusPolicyViolation, "register rejected")
		log.Printf("hub: agent register REJECTED id=%q from %s", reg.MachineID, clientIP(r))
		return
	}
```

`agent` 构造加 `cred: reg.Cred`：

```go
	a := &agent{
		id: reg.MachineID, name: reg.Name, token: reg.Token, cred: reg.Cred, ws: ws,
		phones: make(map[string]*phone), fileReqs: make(map[string]chan frame),
		devices: make(map[string]string),
	}
```

`registered` 回执后发宽限提醒（`_ = a.write(ctx, frame{T: "registered"})` 之后）：

```go
	if notice != "" {
		_ = a.write(ctx, frame{T: "notice", Message: notice})
	}
```

3e. 新增 `authAgent`（放在 `handleAgent` 上方）：

```go
// authAgent validates a register frame. License mode checks the per-machine
// credential against the roster (the shared agentKey is NOT accepted there);
// self-host mode keeps the constant-time shared-key check. Returns a human
// notice ("" if none) — non-empty during the expiry grace window.
func (h *Hub) authAgent(reg frame) (notice string, ok bool) {
	if h.Lic == nil {
		return "", ctEq(reg.AgentKey, h.agentKey)
	}
	res, err := h.Lic.CheckAgent(reg.MachineID, reg.Cred)
	if err != nil {
		log.Printf("hub: license register denied id=%q: %v", reg.MachineID, err)
		return "", false
	}
	h.Lic.TouchLastSeen(reg.MachineID)
	return res.Message, true
}
```

- [ ] **Step 4: 跑测试确认通过（含既有回归）**

Run: `cd bridge && go test ./internal/hub/ -v`
Expected: PASS —— 新增 4 个 license 测试 + 既有全部测试（self-host 模式 `Lic == nil` 行为不变）

- [ ] **Step 5: Commit**

```bash
cd bridge && git add internal/hub/hub.go internal/hub/license_test.go
git commit -m "feat(hub): license-mode agent registration via per-machine credentials"
```

---

### Task 8: hub `/api/activate` 端点（api.go）

**Files:**
- Create: `bridge/internal/hub/api.go`
- Modify: `bridge/internal/hub/hub.go`（Handler() 加路由）
- Test: `bridge/internal/hub/license_test.go`（追加）

- [ ] **Step 1: 追加失败测试**

```go
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
```

（import 需补 `"net/http"`、`"strings"`、`"encoding/json"`。）

- [ ] **Step 2: 跑测试确认失败**

Run: `cd bridge && go test ./internal/hub/ -run TestActivateEndpoint -v`
Expected: FAIL（404 — 路由不存在；或编译错）

- [ ] **Step 3: 写 `api.go`（activate 部分）**

```go
// api.go — license-mode HTTP control endpoints. Every handler 404s when the
// hub runs without a license store (self-host mode), so the public surface is
// unchanged unless HUB_LICENSE_MODE is on.
//
//	POST /api/activate            {key, machineId, name, fpHint}
//	                              → {credential, used, limit, reused}
//	GET  /api/roster              auth like /file (legacy ?token= / device-auth
//	                              ?ticket=) → the caller's account's machines
//	POST /api/roster/deactivate   same auth + {machineId} → frees the slot,
//	                              kicks the agent (swap rate limit applies)
package hub

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/coder/websocket"

	"github.com/yunyuchen/codex-remote/bridge/internal/license"
)

type activateReq struct {
	Key       string `json:"key"`
	MachineID string `json:"machineId"`
	Name      string `json:"name"`
	FpHint    string `json:"fpHint"`
}

func (h *Hub) handleActivate(w http.ResponseWriter, r *http.Request) {
	if h.Lic == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req activateReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	res, err := h.Lic.Activate(req.Key, req.MachineID, req.Name, req.FpHint)
	if err != nil {
		log.Printf("hub: activate DENIED machine=%q from %s: %v", req.MachineID, clientIP(r), err)
		writeLicErr(w, err)
		return
	}
	if res.Reused {
		// reinstall reissued the credential — drop any stale link so the fresh
		// install can register immediately instead of fighting a zombie.
		h.kickAgent(res.MachineID, "credential reissued")
	}
	log.Printf("hub: activated machine=%q (%d/%d) from %s", res.MachineID, res.Used, res.Limit, clientIP(r))
	writeJSON(w, map[string]any{
		"credential": res.Credential,
		"used":       res.Used,
		"limit":      res.Limit,
		"reused":     res.Reused,
	})
}

// kickAgent closes a machine's live agent link, if any (deactivation, reissue).
func (h *Hub) kickAgent(machineID, reason string) {
	if a := h.agentByID(machineID); a != nil {
		_ = a.ws.Close(websocket.StatusPolicyViolation, reason)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writeLicErr maps license errors onto HTTP statuses with a JSON body the
// activation CLI / app can show verbatim.
func writeLicErr(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	switch {
	case errors.Is(err, license.ErrBadKey), errors.Is(err, license.ErrRevoked),
		errors.Is(err, license.ErrExpired), errors.Is(err, license.ErrBadCredential):
		code = http.StatusForbidden
	case errors.Is(err, license.ErrRosterFull), errors.Is(err, license.ErrMachineTaken):
		code = http.StatusConflict
	case errors.Is(err, license.ErrSwapLimit):
		code = http.StatusTooManyRequests
	case errors.Is(err, license.ErrUnknownMachine):
		code = http.StatusNotFound
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
```

`hub.go` 的 `Handler()` 加路由（`/machines` 行后）：

```go
	mux.HandleFunc("/api/activate", h.handleActivate)
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd bridge && go test ./internal/hub/ -run TestActivateEndpoint -v`
Expected: PASS（两个）

- [ ] **Step 5: Commit**

```bash
cd bridge && git add internal/hub/api.go internal/hub/hub.go internal/hub/license_test.go
git commit -m "feat(hub): POST /api/activate issues machine credentials"
```

---

### Task 9: hub 名册端点 — `/api/roster` 与 `/api/roster/deactivate`

**Files:**
- Modify: `bridge/internal/hub/api.go`
- Modify: `bridge/internal/hub/hub.go`（Handler() 加两条路由）
- Test: `bridge/internal/hub/license_test.go`（追加）

- [ ] **Step 1: 追加失败测试**

```go
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
	// unknown machine → 404
	resp, _ = postJSON(t, srv.URL+"/api/roster/deactivate?machine="+testMachine+"&token=tok1", `{"machineId":"ghost"}`)
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusUnauthorized {
		// after kicking testMachine the legacy auth machine is offline; both
		// statuses are acceptable here — assert it is NOT a success
		t.Fatalf("ghost deactivate: status %d", resp.StatusCode)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd bridge && go test ./internal/hub/ -run TestRoster -v`
Expected: FAIL（404 路由不存在）

- [ ] **Step 3: `api.go` 追加名册端点**

```go
// rosterAccount resolves the caller to an account id using material a phone
// already holds: the legacy per-machine bearer token (machine must be online)
// or the device-auth /file session ticket. A caller can only ever reach its
// own account. Fallback when every machine is offline/lost: admin CLI.
func (h *Hub) rosterAccount(r *http.Request) (int64, bool) {
	var machineID string
	if h.RequireDeviceAuth {
		machineID = h.resolveFileTicket(r.URL.Query().Get("ticket"))
	} else if a := h.findAgent(r.URL.Query().Get("machine"), r.URL.Query().Get("token")); a != nil {
		machineID = a.id
	}
	if machineID == "" {
		return 0, false
	}
	aid, err := h.Lic.AccountIDForMachine(machineID)
	if err != nil {
		return 0, false
	}
	return aid, true
}

func (h *Hub) handleRoster(w http.ResponseWriter, r *http.Request) {
	if h.Lic == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	aid, ok := h.rosterAccount(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ms, err := h.Lic.Roster(aid)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	type entry struct {
		MachineID string `json:"machineId"`
		Name      string `json:"name"`
		Online    bool   `json:"online"`
		LastSeen  int64  `json:"lastSeen,omitempty"` // unix seconds, 0 = never
	}
	out := make([]entry, 0, len(ms))
	for _, m := range ms {
		var seen int64
		if !m.LastSeen.IsZero() {
			seen = m.LastSeen.Unix()
		}
		out = append(out, entry{m.MachineID, m.Name, h.agentByID(m.MachineID) != nil, seen})
	}
	writeJSON(w, out)
}

func (h *Hub) handleRosterDeactivate(w http.ResponseWriter, r *http.Request) {
	if h.Lic == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	aid, ok := h.rosterAccount(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		MachineID string `json:"machineId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := h.Lic.Deactivate(aid, req.MachineID); err != nil {
		writeLicErr(w, err)
		return
	}
	h.kickAgent(req.MachineID, "deactivated")
	log.Printf("hub: machine %q deactivated (account %d) from %s", req.MachineID, aid, clientIP(r))
	writeJSON(w, map[string]string{"status": "ok"})
}
```

`hub.go` 的 `Handler()` 追加：

```go
	mux.HandleFunc("/api/roster", h.handleRoster)
	mux.HandleFunc("/api/roster/deactivate", h.handleRosterDeactivate)
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd bridge && go test ./internal/hub/ -v`
Expected: PASS（全部，含回归）

- [ ] **Step 5: Commit**

```bash
cd bridge && git add internal/hub/api.go internal/hub/hub.go internal/hub/license_test.go
git commit -m "feat(hub): roster list + self-service deactivate endpoints (phone-auth gated)"
```

---

### Task 10: hub 周期重验 — RunRecheck（recheck.go）

**Files:**
- Create: `bridge/internal/hub/recheck.go`
- Test: `bridge/internal/hub/license_test.go`（追加）

- [ ] **Step 1: 追加失败测试**

```go
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
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd bridge && go test ./internal/hub/ -run TestRecheck -v`
Expected: FAIL（`recheckOnce` 未定义）

- [ ] **Step 3: 写 `recheck.go`**

```go
// recheck.go — periodic revalidation of connected agents against the license
// store. Register-time checks alone would let an expired/revoked/deactivated
// machine stay online for as long as its WebSocket survives; this loop bounds
// that exposure to one interval (default hourly — stricter than the spec's
// daily floor, so admin revocations land within the hour). Grace-window agents
// get a renewal notice each pass instead of a kick.
package hub

import (
	"context"
	"log"
	"time"

	"github.com/coder/websocket"
)

// RunRecheck blocks, revalidating every connected agent each interval. No-op
// (returns immediately) outside license mode.
func (h *Hub) RunRecheck(ctx context.Context, interval time.Duration) {
	if h.Lic == nil {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.recheckOnce()
		}
	}
}

func (h *Hub) recheckOnce() {
	h.mu.Lock()
	agents := make([]*agent, 0, len(h.agents))
	for _, a := range h.agents {
		agents = append(agents, a)
	}
	h.mu.Unlock()
	for _, a := range agents {
		res, err := h.Lic.CheckAgent(a.id, a.cred)
		if err != nil {
			log.Printf("hub: recheck kicking id=%q: %v", a.id, err)
			// closing the ws unblocks the agent's read loop; its defer cleans up
			// phones + registration exactly like a normal disconnect.
			_ = a.ws.Close(websocket.StatusPolicyViolation, "license check failed")
			continue
		}
		h.Lic.TouchLastSeen(a.id)
		if res.Message != "" {
			_ = a.write(context.Background(), frame{T: "notice", Message: res.Message})
		}
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd bridge && go test ./internal/hub/ -v && cd bridge && go vet ./internal/hub/`
Expected: PASS + vet 干净

- [ ] **Step 5: Commit**

```bash
cd bridge && git add internal/hub/recheck.go internal/hub/license_test.go
git commit -m "feat(hub): hourly license recheck kicks expired/revoked/deactivated agents"
```

---

### Task 11: codexhub 装配 — HUB_LICENSE_MODE + admin 子命令

**Files:**
- Modify: `bridge/cmd/codexhub/main.go`
- Create: `bridge/cmd/codexhub/admin.go`

- [ ] **Step 1: 改 `main.go`**

文件顶部 doc comment 的 env 清单追加两行：

```go
//	HUB_LICENSE_MODE       "1" switches agent registration from the shared
//	                       HUB_AGENT_KEY to per-machine credentials backed by
//	                       the license db (see internal/license); also enables
//	                       /api/activate and /api/roster*.
//	HUB_DB                 license db path (default hub.db; license mode only)
//
// Admin subcommands (license mode ops; run on the hub host, safe alongside a
// running hub thanks to SQLite WAL):
//
//	codexhub admin issue-key  [-plan beta] [-machines 3] [-months 6]
//	codexhub admin list-keys
//	codexhub admin revoke-key  crk_...
//	codexhub admin extend-key  -months 6 crk_...
//	codexhub admin unbind      <machine-id>
```

`main()` 开头加 admin 分发，并在 `h.RequireDeviceAuth` 装配后加 license 装配：

```go
func main() {
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		adminMain(os.Args[2:])
		return
	}
	// ... existing addr/agentKey/h setup unchanged ...

	if os.Getenv("HUB_LICENSE_MODE") == "1" {
		dbPath := os.Getenv("HUB_DB")
		if dbPath == "" {
			dbPath = "hub.db"
		}
		lic, err := license.Open(dbPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "FATAL: license db:", err)
			os.Exit(1)
		}
		h.Lic = lic
		go h.RunRecheck(context.Background(), time.Hour)
		fmt.Printf("codex-hub: license mode ON (db=%s) — agents register with machine credentials\n", dbPath)
	}
	// ... existing srv/ListenAndServe unchanged ...
}
```

import 增加 `"context"` 与 `"github.com/yunyuchen/codex-remote/bridge/internal/license"`。

- [ ] **Step 2: 写 `admin.go`**

```go
// admin.go — codexhub's license-ops subcommands. They open the SQLite db
// directly (no hub API): simplest possible ops surface for the invite-only
// beta, and WAL makes it safe next to a running hub.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/yunyuchen/codex-remote/bridge/internal/license"
)

func adminUsage() {
	fmt.Fprintln(os.Stderr, `usage: codexhub admin <command> [flags] [args]

  issue-key   [-db PATH] [-plan beta] [-machines 3] [-months 6]   mint a key (printed ONCE)
  list-keys   [-db PATH]                                          table of keys + usage
  revoke-key  [-db PATH] crk_...                                  bar a key (machines kicked within the hour)
  extend-key  [-db PATH] [-months 6] crk_...                      push expiry out
  unbind      [-db PATH] <machine-id>                             operator unbind (no swap limit)

-db defaults to $HUB_DB, then hub.db.`)
	os.Exit(2)
}

func dbFlag(fs *flag.FlagSet) *string {
	def := os.Getenv("HUB_DB")
	if def == "" {
		def = "hub.db"
	}
	return fs.String("db", def, "license db path")
}

func openSvc(path string) *license.Service {
	svc, err := license.Open(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "admin: open db:", err)
		os.Exit(1)
	}
	return svc
}

func adminFatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "admin:", err)
		os.Exit(1)
	}
}

func adminMain(args []string) {
	if len(args) == 0 {
		adminUsage()
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "issue-key":
		fs := flag.NewFlagSet("issue-key", flag.ExitOnError)
		db := dbFlag(fs)
		plan := fs.String("plan", "beta", "plan label")
		machines := fs.Int("machines", 3, "machine limit")
		months := fs.Int("months", 6, "validity in months")
		_ = fs.Parse(rest)
		svc := openSvc(*db)
		defer svc.Close()
		key, err := svc.IssueKey(*plan, *machines, *months)
		adminFatal(err)
		fmt.Println(key)
		fmt.Fprintln(os.Stderr, "shown ONCE — only its hash is stored; deliver it to the user now")
	case "list-keys":
		fs := flag.NewFlagSet("list-keys", flag.ExitOnError)
		db := dbFlag(fs)
		_ = fs.Parse(rest)
		svc := openSvc(*db)
		defer svc.Close()
		infos, err := svc.ListKeys()
		adminFatal(err)
		fmt.Printf("%-14s %-8s %5s %4s  %-10s %s\n", "PREFIX", "PLAN", "LIMIT", "USED", "EXPIRES", "REVOKED")
		for _, ki := range infos {
			rev := "-"
			if ki.Revoked {
				rev = "REVOKED"
			}
			fmt.Printf("%-14s %-8s %5d %4d  %-10s %s\n",
				ki.Prefix+"…", ki.Plan, ki.Limit, ki.Used, ki.ExpiresAt.Format("2006-01-02"), rev)
		}
	case "revoke-key":
		fs := flag.NewFlagSet("revoke-key", flag.ExitOnError)
		db := dbFlag(fs)
		_ = fs.Parse(rest)
		if fs.NArg() != 1 {
			adminUsage()
		}
		svc := openSvc(*db)
		defer svc.Close()
		adminFatal(svc.RevokeKey(fs.Arg(0)))
		fmt.Println("revoked — connected machines are kicked at the next hourly recheck")
	case "extend-key":
		fs := flag.NewFlagSet("extend-key", flag.ExitOnError)
		db := dbFlag(fs)
		months := fs.Int("months", 6, "months to add")
		_ = fs.Parse(rest)
		if fs.NArg() != 1 {
			adminUsage()
		}
		svc := openSvc(*db)
		defer svc.Close()
		adminFatal(svc.ExtendKey(fs.Arg(0), *months))
		fmt.Println("extended")
	case "unbind":
		fs := flag.NewFlagSet("unbind", flag.ExitOnError)
		db := dbFlag(fs)
		_ = fs.Parse(rest)
		if fs.NArg() != 1 {
			adminUsage()
		}
		svc := openSvc(*db)
		defer svc.Close()
		adminFatal(svc.AdminDeactivate(fs.Arg(0)))
		fmt.Println("unbound — slot freed; the machine is kicked at the next hourly recheck")
	default:
		adminUsage()
	}
}
```

- [ ] **Step 3: 构建 + 冒烟**

```bash
cd bridge && go build ./cmd/codexhub && go vet ./cmd/codexhub
TMPDB=$(mktemp -d)/hub.db
KEY=$(./codexhub admin issue-key -db "$TMPDB" -plan beta -machines 2 -months 6)
echo "issued: $KEY"
./codexhub admin list-keys -db "$TMPDB"
./codexhub admin revoke-key -db "$TMPDB" "$KEY"
./codexhub admin list-keys -db "$TMPDB"   # 应显示 REVOKED
rm -f ./codexhub
```

Expected: key 打印一次；list 两次输出，第二次带 REVOKED。

- [ ] **Step 4: Commit**

```bash
cd bridge && git add cmd/codexhub/
git commit -m "feat(codexhub): license-mode wiring (HUB_LICENSE_MODE/HUB_DB) + admin subcommands"
```

---

### Task 12: 硬件指纹 — provision.FingerprintHint

**Files:**
- Create: `bridge/internal/provision/fingerprint.go`
- Create: `bridge/internal/provision/fingerprint_darwin.go`
- Create: `bridge/internal/provision/fingerprint_windows.go`
- Create: `bridge/internal/provision/fingerprint_other.go`
- Test: `bridge/internal/provision/fingerprint_test.go`
- Modify: `bridge/go.mod`（`golang.org/x/sys` 由 indirect 升为直接依赖 — Windows registry）

- [ ] **Step 1: 写失败测试** `fingerprint_test.go`

```go
package provision

import "testing"

func TestFingerprintHintStableAndOpaque(t *testing.T) {
	a, b := FingerprintHint(), FingerprintHint()
	if a != b {
		t.Fatalf("not stable across calls: %q vs %q", a, b)
	}
	// "" is legal (unsupported platform / lookup failure); when present it must
	// be the 32-hex-char truncated hash, never a raw UUID
	if a != "" && len(a) != 32 {
		t.Fatalf("want 32 hex chars or empty, got %q (len %d)", a, len(a))
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd bridge && go test ./internal/provision/ -run TestFingerprint -v`
Expected: FAIL（`FingerprintHint` 未定义）

- [ ] **Step 3: 写四个文件**

`fingerprint.go`：

```go
package provision

import (
	"crypto/sha256"
	"encoding/hex"
)

// FingerprintHint returns a stable, non-reversible hardware hint (truncated
// SHA-256 of the platform UUID) or "" when unavailable. Activation uses it to
// recognize a reinstalled machine and reuse its roster slot without burning a
// swap; it is NEVER a hard gate (VMs / board swaps would misfire) — see the
// license package doc.
func FingerprintHint() string {
	u := platformUUID()
	if u == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("fp:" + u))
	return hex.EncodeToString(sum[:16])
}
```

`fingerprint_darwin.go`：

```go
//go:build darwin

package provision

import (
	"os/exec"
	"strings"
)

// platformUUID reads the IOPlatformUUID — stable across reinstalls, per-device.
func platformUUID() string {
	out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "IOPlatformUUID") {
			continue
		}
		// `      "IOPlatformUUID" = "XXXX-..."` → quote-split index 3
		parts := strings.Split(line, "\"")
		if len(parts) >= 4 {
			return parts[3]
		}
	}
	return ""
}
```

`fingerprint_windows.go`：

```go
//go:build windows

package provision

import "golang.org/x/sys/windows/registry"

// platformUUID reads HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid — stable
// across reinstalls that keep the disk, per-device.
func platformUUID() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		return ""
	}
	return v
}
```

`fingerprint_other.go`：

```go
//go:build !darwin && !windows

package provision

// platformUUID: no portable stable id on other platforms; activation then
// simply skips the reinstall-reuse fast path.
func platformUUID() string { return "" }
```

- [ ] **Step 4: 依赖 + 测试 + 交叉编译验证**

```bash
cd bridge && go get golang.org/x/sys@latest
go test ./internal/provision/ -v
GOOS=windows GOARCH=amd64 go build ./...   # 验证 windows 分支编译
GOOS=linux  GOARCH=amd64 go build ./...    # 验证 other 分支编译（hub 服务器）
```

Expected: 测试 PASS（macOS 上 hint 非空且稳定）；两个交叉编译均成功。

- [ ] **Step 5: Commit**

```bash
cd bridge && git add go.mod go.sum internal/provision/fingerprint*
git commit -m "feat(provision): per-OS hardware fingerprint hint for slot reuse"
```

---

### Task 13: bridge 端 — 凭证注册、notice 日志、activate 子命令

**Files:**
- Modify: `bridge/internal/bridge/agent.go`
- Modify: `bridge/cmd/codexbridge/main.go`
- Create: `bridge/cmd/codexbridge/activate.go`

- [ ] **Step 1: 改 `agent.go`（三处）**

1a. `hubFrame` 结构体 `AgentKey` 行后加：

```go
	Cred      string `json:"cred,omitempty"` // license mode: per-machine credential
```

1b. `AgentConfig` 加字段：

```go
	// Cred is the per-machine credential from `codexbridge activate` (license
	// mode). When set it replaces AgentKey as the hub register secret.
	Cred string
```

1c. `runAgentOnce` 的 register 帧带上 Cred：

```go
	if err := a.writeFrame(cctx, hubFrame{
		T: "register", MachineID: cfg.MachineID, Name: cfg.Name, Token: cfg.Token,
		AgentKey: cfg.AgentKey, Cred: cfg.Cred,
	}); err != nil {
		return err
	}
```

1d. 读循环 switch 加 notice 分支（`case "error":` 之后）：

```go
		case "notice":
			// hub-side renewal reminders (expiry grace) — surface in the log/tray
			log.Printf("agent: hub notice: %s", f.Message)
```

- [ ] **Step 2: 改 `cmd/codexbridge/main.go` 的 agent 子命令**

2a. flag 定义区（`agentKeyFile` 行后）追加：

```go
		cred := fs.String("cred", "", "machine credential from `codexbridge activate` (else $CODEX_MACHINE_CRED, else -cred-file)")
		credFile := fs.String("cred-file", "", "read the machine credential from this file (else $CODEX_MACHINE_CRED_FILE)")
```

2b. `runAgent` 调用与签名加传参：

```go
		runAgent(*codexBin, *hubURL, *id, *name, *token, *agentKey, *cred, *tokenFile, *agentKeyFile, *credFile, *logPath)
```

```go
func runAgent(codexBin, hubURL, id, name, token, agentKey, cred, tokenFile, agentKeyFile, credFile, logPath string) {
```

2c. `runAgent` 内秘密解析：在 agentKey 解析块后追加 cred 解析，并把原 `agentKey == ""` 的 fatal 替换为合并检查：

```go
	if cred == "" {
		cred = os.Getenv("CODEX_MACHINE_CRED")
	}
	if cred == "" {
		if credFile == "" {
			credFile = os.Getenv("CODEX_MACHINE_CRED_FILE")
		}
		cred = readSecretFile(credFile)
	}
	if agentKey == "" && cred == "" {
		fatal("agent", fmt.Errorf("no hub register secret: license mode needs $CODEX_MACHINE_CRED / -cred / -cred-file (run `codexbridge activate` first); self-host needs $CODEX_AGENT_KEY / -agent-key / -agent-key-file"))
	}
```

（原来的 `if agentKey == "" { fatal(...) }` 整块删除，agentKey 的 env/file 解析保留。）

2d. `RunAgent` 调用传 Cred：

```go
	srv.RunAgent(ctx, bridge.AgentConfig{HubURL: hubURL, MachineID: id, Name: name, Token: token, AgentKey: agentKey, Cred: cred})
```

2e. usage 行与文件顶 doc comment 增加 `activate` 子命令说明：

```go
		fmt.Fprintln(os.Stderr, "usage: codexbridge [-codex PATH] <list|read THREAD_ID|demo-turn|serve [-addr HOST:PORT]|agent ...|activate ...|reset-token>")
```

2f. `main()` 的子命令分发加（`agent` 块之后）：

```go
	if args[0] == "activate" {
		fs := flag.NewFlagSet("activate", flag.ExitOnError)
		hubURL := fs.String("hub", "", "hub base URL, e.g. https://relay.example.com")
		key := fs.String("key", "", "license key (crk_...)")
		id := fs.String("id", "", "machine id the phone selects (e.g. office-mac)")
		name := fs.String("name", "", "human label shown in the app (default: id)")
		out := fs.String("out", "", "write the credential to this file (0600) instead of stdout")
		_ = fs.Parse(args[1:])
		runActivate(*hubURL, *key, *id, *name, *out)
		return
	}
```

- [ ] **Step 3: 写 `cmd/codexbridge/activate.go`**

```go
// activate.go — `codexbridge activate` binds this machine to a license key via
// the hub's POST /api/activate and stores the per-machine credential the agent
// registers with (license mode replaces the shared CODEX_AGENT_KEY). The
// credential is shown/written ONCE; the hub keeps only its hash.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/yunyuchen/codex-remote/bridge/internal/provision"
)

func runActivate(hubURL, key, id, name, out string) {
	if hubURL == "" || key == "" || id == "" {
		fatal("activate", fmt.Errorf("need -hub, -key and -id"))
	}
	if name == "" {
		name = id
	}
	base := strings.TrimSuffix(hubURL, "/")
	base = strings.Replace(base, "ws://", "http://", 1)
	base = strings.Replace(base, "wss://", "https://", 1)

	body, err := json.Marshal(map[string]string{
		"key": key, "machineId": id, "name": name,
		"fpHint": provision.FingerprintHint(),
	})
	if err != nil {
		fatal("activate", err)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(base+"/api/activate", "application/json", bytes.NewReader(body))
	if err != nil {
		fatal("activate", err)
	}
	defer resp.Body.Close()

	var res struct {
		Credential string `json:"credential"`
		Used       int    `json:"used"`
		Limit      int    `json:"limit"`
		Reused     bool   `json:"reused"`
		Error      string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		fatal("activate", fmt.Errorf("bad hub response (HTTP %d): %v", resp.StatusCode, err))
	}
	if resp.StatusCode != http.StatusOK {
		fatal("activate", fmt.Errorf("hub refused (HTTP %d): %s", resp.StatusCode, res.Error))
	}

	if out != "" {
		if err := os.WriteFile(out, []byte(res.Credential+"\n"), 0o600); err != nil {
			fatal("activate", err)
		}
		fmt.Printf("✓ 已激活 %q(%d/%d 台)— 凭证已写入 %s(只此一次,hub 仅存哈希)\n", id, res.Used, res.Limit, out)
	} else {
		fmt.Printf("✓ 已激活 %q(%d/%d 台)。机器凭证(只显示一次):\n%s\n", id, res.Used, res.Limit, res.Credential)
	}
	if res.Reused {
		fmt.Println("  (检测到同一台机器重装 — 复用了原名额,旧凭证已作废)")
	}
	fmt.Printf("  启动: codexbridge agent -hub %s/agent -id %s -cred-file <凭证文件> -token <手机配对token>\n",
		strings.Replace(strings.Replace(base, "http://", "ws://", 1), "https://", "wss://", 1), id)
}
```

- [ ] **Step 4: 构建 + 端到端冒烟（本地全链路）**

```bash
cd bridge && go build ./cmd/... && go vet ./... && go test ./...
```

Expected: 全绿。然后手动端到端（两个终端）：

```bash
# 终端 A:license 模式 hub
cd bridge && go build -o /tmp/codexhub ./cmd/codexhub
KEY_DB=/tmp/hub-e2e.db
HUB_ADDR=127.0.0.1:8090 HUB_AGENT_KEY=devagentkey HUB_LICENSE_MODE=1 HUB_DB=$KEY_DB /tmp/codexhub &
KEY=$(/tmp/codexhub admin issue-key -db $KEY_DB -machines 1 -months 1)

# 终端 B:激活 + agent 注册
cd bridge && go run ./cmd/codexbridge activate -hub http://127.0.0.1:8090 -key "$KEY" -id e2e-mac -out /tmp/e2e-cred
CODEX_MACHINE_TOKEN=devtok go run ./cmd/codexbridge agent -hub ws://127.0.0.1:8090/agent -id e2e-mac -cred-file /tmp/e2e-cred
# 预期:hub 日志出现 `agent registered id="e2e-mac"`
# 验证名册/解绑:
curl 'http://127.0.0.1:8090/api/roster?machine=e2e-mac&token=devtok'
curl -X POST 'http://127.0.0.1:8090/api/roster/deactivate?machine=e2e-mac&token=devtok' -d '{"machineId":"e2e-mac"}'
# 预期:agent 被踢下线,重连被拒(register rejected)
```

- [ ] **Step 5: Commit**

```bash
cd bridge && git add internal/bridge/agent.go cmd/codexbridge/
git commit -m "feat(bridge): machine-credential registration + codexbridge activate"
```

---

### Task 14: 文档同步 — 包注释、CLAUDE.md、DEPLOY

**Files:**
- Modify: `bridge/internal/hub/hub.go`（包注释）
- Modify: `CLAUDE.md`（env 清单 + 安全模型一行）
- Modify: `bridge/deploy/HUB.md`（license 模式运维段）

- [ ] **Step 1: `hub.go` 包注释**在 "Phone authentication has two modes" 段后追加：

```go
// Agent (machine) authentication likewise has two modes:
//   - Self-host (default): every machine presents the shared HUB_AGENT_KEY.
//   - License (HUB_LICENSE_MODE=1): each machine presents the per-machine
//     credential minted by POST /api/activate against a license key; the
//     internal/license roster (machine limit, swap rate limit, expiry grace)
//     decides admission, re-checked hourly for long-lived links. /api/roster*
//     lets a paired phone list and self-service-unbind its account's machines.
```

- [ ] **Step 2: `CLAUDE.md`** 的 env 行追加 `HUB_LICENSE_MODE`、`HUB_DB`，并在 hub 描述处加一句「license 模式（订阅内测）见 `internal/license` 包注释与 `docs/superpowers/specs/2026-06-10-subscription-machine-binding-design.md`」。

- [ ] **Step 3: `bridge/deploy/HUB.md`** 增加「License 模式（内测发 key）」一节：开启 env、issue-key/激活/解绑命令示例、备份 `hub.db` 的提醒（每日 cron `sqlite3 hub.db ".backup ..."` 或直接 cp WAL checkpoint 后的文件）。

- [ ] **Step 4: 全量回归 + Commit**

```bash
cd bridge && go build ./cmd/... && go vet ./... && go test ./...
cd .. && git add bridge/internal/hub/hub.go CLAUDE.md bridge/deploy/HUB.md
git commit -m "docs: license-mode protocol + ops notes (hub comment, CLAUDE.md, HUB.md)"
```

Expected: 构建/vet/测试全绿后提交。

---

## 完成定义（Definition of Done)

- [ ] `cd bridge && go test ./...` 全绿；`go vet ./...` 干净
- [ ] Task 13 Step 4 的端到端冒烟全部按预期(激活→注册→roster→解绑→踢线→重连被拒)
- [ ] self-host 回归:不设 `HUB_LICENSE_MODE` 时,既有 hub 测试全绿,`/api/*` 全部 404
- [ ] 凭证/key 明文只在签发瞬间出现过一次;db 中只有哈希(`sqlite3 hub.db 'select * from license_keys'` 人工抽查)
- [ ] `server.go` 零改动(`git diff --stat` 确认)

## Plan 2 预告(另写计划)

Flutter App 机器列表页 + 解绑(调 `/api/roster*`,legacy 用现有 token,device-auth 用 auth_ok 的 ticket);小程序 `relay.js` 镜像;菜单栏托盘的「输入订阅码」UI(包装 `codexbridge activate`)。





