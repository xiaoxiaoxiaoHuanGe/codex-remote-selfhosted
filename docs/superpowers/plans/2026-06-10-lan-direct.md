# LAN 直连 + 公网分层 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 同一 WiFi 下手机直连桌面 codexbridge（免费档），订阅激活的机器可走公网中继，auto 模式竞速自动选路。

**Architecture:** bridge agent 模式新增 LAN 监听（复用现有 `Server.Handler()` 与全部安全 clamp），通过新 `lanInfo` 帧 + 托盘 QR `lan=`/`pub=` 参数把候选地址下发到手机本地缓存；手机端新增 `ConnectionRacer` 做 happy-eyeballs 竞速（LAN 先拨、中继 300ms 后），配对首连不竞速。hub 零改动。

**Tech Stack:** Go 1.26（bridge/menubar）、Flutter/Dart（app）、JSON Schema 手机协议（文本 WS 帧）。

**Spec:** `docs/superpowers/specs/2026-06-10-lan-direct-design.md`

**验证命令**（每个任务的测试步骤都用它们）：
- Go: `cd bridge && go test ./internal/bridge/ -run <Test名> -v`；全量 `go test ./... && go vet ./...`
- Dart: `cd app && flutter test test/<文件> `；全量 `flutter test`

---

### Task 1: Go — LAN 候选枚举 + LAN 监听器（`internal/bridge/lan.go`）

**Files:**
- Create: `bridge/internal/bridge/lan.go`
- Test: `bridge/internal/bridge/lan_test.go`

- [ ] **Step 1: 写失败测试**

```go
// bridge/internal/bridge/lan_test.go
package bridge

import (
	"io"
	"net"
	"net/http"
	"testing"
)

func ipnet(cidr string) net.Addr {
	ip, n, _ := net.ParseCIDR(cidr)
	n.IP = ip
	return n
}

func TestCandidatesFromAddrs(t *testing.T) {
	addrs := []net.Addr{
		ipnet("192.168.1.5/24"),   // 私网 IPv4 → 保留
		ipnet("10.0.0.3/8"),       // 私网 IPv4 → 保留
		ipnet("8.8.8.8/32"),       // 公网 IPv4 → 丢弃
		ipnet("fe80::1/64"),       // IPv6 → 丢弃（v1 仅 IPv4）
		ipnet("127.0.0.1/8"),      // 环回 → 丢弃（IsPrivate=false 兜底）
	}
	got := candidatesFromAddrs(addrs, "8767")
	want := []string{"192.168.1.5:8767", "10.0.0.3:8767"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestStartLANListener(t *testing.T) {
	// "off" 显式关闭
	if got := StartLANListener(&Server{}, "off"); got != "" {
		t.Fatalf("off: want empty port, got %q", got)
	}
	// 正常监听：/healthz 可达（Handler 复用，不触发 ws 鉴权路径）
	s := &Server{tokens: map[string]string{"tok": "default"}, clients: map[*conn]struct{}{}, maxConn: 8}
	port := StartLANListener(s, "127.0.0.1:0")
	if port == "" {
		t.Fatal("want a bound port")
	}
	resp, err := http.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "ok" {
		t.Fatalf("healthz body %q", b)
	}
	// 端口冲突：同端口再监听 → 返回 ""（不 panic 不 fatal）
	if got := StartLANListener(s, "127.0.0.1:"+port); got != "" {
		t.Fatalf("conflict: want empty, got %q", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd bridge && go test ./internal/bridge/ -run 'TestCandidatesFromAddrs|TestStartLANListener' -v`
Expected: FAIL —— `undefined: candidatesFromAddrs` / `undefined: StartLANListener`

- [ ] **Step 3: 最小实现**

```go
// bridge/internal/bridge/lan.go
// LAN direct-connect support: enumerating this machine's private IPv4
// addresses (the candidates pushed to phones via lanInfo and embedded in the
// tray QR's lan= param) and the optional LAN listener agent mode serves them
// on. The listener reuses Server.Handler() verbatim — token gate, turn/cwd/
// approval clamps and the /file allowlists all apply unchanged.
package bridge

import (
	"log"
	"net"
	"net/http"
	"time"
)

// candidatesFromAddrs filters interface addresses down to private (RFC1918)
// IPv4s and joins each with port. Pure — unit-tested directly.
func candidatesFromAddrs(addrs []net.Addr, port string) []string {
	var out []string
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := n.IP.To4()
		if ip4 == nil || !ip4.IsPrivate() {
			continue
		}
		out = append(out, net.JoinHostPort(ip4.String(), port))
	}
	return out
}

// LANCandidates returns "ip:port" for every private IPv4 on an up,
// non-loopback interface — the addresses a phone on the same LAN can reach
// this machine at. Enumerated per call: DHCP can change them mid-run.
func LANCandidates(port string) []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, in := range ifs {
		if in.Flags&net.FlagUp == 0 || in.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := in.Addrs()
		if err != nil {
			continue
		}
		out = append(out, candidatesFromAddrs(addrs, port)...)
	}
	return out
}

// StartLANListener serves s.Handler() on a LAN address (agent mode's direct
// path). addr "off" disables. Listen failure is NON-fatal — the agent keeps
// running relay-only and no candidates get published. Returns the bound port
// ("" when disabled/failed).
func StartLANListener(s *Server, addr string) string {
	if addr == "off" {
		return ""
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("lan: listen %s failed: %v — direct LAN disabled, relay only", addr, err)
		return ""
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		_ = ln.Close()
		return ""
	}
	hs := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		if err := hs.Serve(ln); err != nil {
			log.Printf("lan: listener ended: %v", err)
		}
	}()
	log.Printf("lan: direct listener on %s", ln.Addr())
	return port
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd bridge && go test ./internal/bridge/ -run 'TestCandidatesFromAddrs|TestStartLANListener' -v`
Expected: PASS（2 个测试）

- [ ] **Step 5: Commit**

```bash
git add bridge/internal/bridge/lan.go bridge/internal/bridge/lan_test.go
git commit -m "feat(bridge): LAN 候选枚举 + agent 模式 LAN 监听器"
```

---

### Task 2: Go — `lanInfo` 帧（Server 状态 + 三处发送点 + 协议注释）

**Files:**
- Modify: `bridge/internal/bridge/server.go`（Server struct ~L94-139、NewServer ~L252、handleWS ~L457、头部协议注释）
- Modify: `bridge/internal/bridge/e2ee.go:146`
- Modify: `bridge/internal/bridge/agent.go:276-278`
- Test: `bridge/internal/bridge/lan_test.go`（追加）

- [ ] **Step 1: 写失败测试（追加到 lan_test.go）**

```go
func TestLanInfoFrame(t *testing.T) {
	s := &Server{lanCands: func(port string) []string {
		return []string{"192.168.1.5:" + port}
	}}
	// 未设 lanPort：候选必须为空（不发布假地址），pub 默认 false
	f := s.lanInfoFrame()
	if f["type"] != "lanInfo" {
		t.Fatalf("type = %v", f["type"])
	}
	if c := f["candidates"].([]string); len(c) != 0 {
		t.Fatalf("no lanPort: want empty candidates, got %v", c)
	}
	if f["pub"] != false {
		t.Fatalf("pub default: want false, got %v", f["pub"])
	}
	// 设置后：候选带端口，pub 翻转
	s.SetLANPort("8767")
	s.SetPubEnabled(true)
	f = s.lanInfoFrame()
	c := f["candidates"].([]string)
	if len(c) != 1 || c[0] != "192.168.1.5:8767" {
		t.Fatalf("candidates = %v", c)
	}
	if f["pub"] != true {
		t.Fatalf("pub = %v", f["pub"])
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd bridge && go test ./internal/bridge/ -run TestLanInfoFrame -v`
Expected: FAIL —— `undefined: s.lanInfoFrame` 等

- [ ] **Step 3: 实现 Server 状态与帧构造**

在 `server.go` 的 `Server` struct 末尾（`devicesChanged chan struct{}` 之后）加：

```go
	// LAN direct-connect state. lanPort is the port StartLANListener bound (""
	// = no listener → lanInfo advertises no candidates). pubEnabled tells
	// phones whether this machine's relay tier is active (license cred or
	// agent key present) — UX only, the hub enforces the tier server-side.
	// lanCands is swappable for tests (defaults to LANCandidates).
	lanMu      sync.Mutex
	lanPort    string
	pubEnabled bool
	lanCands   func(port string) []string
```

在 `NewServer` 的 struct 字面量里加一行（`allowAutoReview:` 之后）：

```go
		lanCands: LANCandidates,
```

在 `lan.go` 末尾追加：

```go
// SetLANPort publishes the LAN listener port lanInfo advertises ("" = none).
func (s *Server) SetLANPort(p string) { s.lanMu.Lock(); s.lanPort = p; s.lanMu.Unlock() }

// SetPubEnabled records whether the relay (public) tier is active.
func (s *Server) SetPubEnabled(b bool) { s.lanMu.Lock(); s.pubEnabled = b; s.lanMu.Unlock() }

// lanInfoFrame builds the lanInfo frame pushed to every fresh phone session:
// the LAN addresses the phone may dial directly + the relay-tier flag. An
// empty candidate list is authoritative — the phone clears its cache.
func (s *Server) lanInfoFrame() map[string]any {
	s.lanMu.Lock()
	port, pub, cands := s.lanPort, s.pubEnabled, s.lanCands
	s.lanMu.Unlock()
	list := []string{}
	if port != "" && cands != nil {
		list = append(list, cands(port)...)
	}
	return map[string]any{"type": "lanInfo", "candidates": list, "pub": pub}
}

// sendLanInfo pushes lanInfo to one fresh session. Called at the SAME moments
// as resyncTo (post-arm under E2EE, on connect otherwise) so it rides the
// "session can carry app frames" instant on both transports; via the hub it is
// an ordinary msg frame the relay forwards blindly.
func (s *Server) sendLanInfo(c *conn) { c.push(s.lanInfoFrame()) }
```

- [ ] **Step 4: 接三处发送点（与 resyncTo 同位）**

`server.go` handleWS（~L457）：

```go
	if !s.e2eeOn() {
		s.resyncTo(c)
		s.sendLanInfo(c)
	}
```

`e2ee.go` routeInbound 首帧武装分支（L146 `s.resyncTo(c)` 之后加一行）：

```go
		s.resyncTo(c)
		s.sendLanInfo(c)
```

`agent.go` open（~L276）：

```go
	if !a.srv.e2eeOn() {
		a.srv.resyncTo(c)
		a.srv.sendLanInfo(c)
	}
```

- [ ] **Step 5: 更新 server.go 头部协议注释**

在头部注释的 bridge→phone 帧清单处（grep `// Outbound` 或帧列表段落）追加一行说明：

```
//	{"type":"lanInfo","candidates":["ip:port",...],"pub":bool}
//	    — pushed once per fresh session (alongside the approval resync): the
//	    machine's private-IPv4 direct-connect candidates (empty list = LAN
//	    listener off; authoritative, the phone clears its cache) and whether
//	    the relay tier is active (UX hint only — the hub enforces the tier).
```

并在安全模型段落追加：

```
//   - LAN listener (agent mode, CODEX_LAN_ADDR, default :8767, "off"
//     disables): serves this same Handler on the LAN. Identical token gate
//     and clamps — no separate code path, no weakened invariant.
```

- [ ] **Step 6: 跑测试确认通过**

Run: `cd bridge && go test ./internal/bridge/ -run TestLanInfoFrame -v && go vet ./...`
Expected: PASS，vet 无报错

- [ ] **Step 7: Commit**

```bash
git add bridge/internal/bridge/
git commit -m "feat(bridge): lanInfo 帧 —— 向手机下发 LAN 直连候选与公网档标志"
```

---

### Task 3: Go — runAgent 接入监听 + 未激活也先开 LAN（cred 等待后移）

**Files:**
- Modify: `bridge/cmd/codexbridge/main.go`（runAgent L300-409、runServe L210-270）

- [ ] **Step 1: runAgent 重排**

当前 L357-372 的 cred 等待块在 appserver/Server 创建**之前**，未激活用户会一直阻塞、LAN 监听无法启动。改为：

1. 秘密解析段（L327-356 不动）之后，把原 L357-372 整块**替换**为早期硬校验：

```go
	if agentKey == "" && cred == "" && credFile == "" {
		fatal("agent", fmt.Errorf("no hub register secret: license mode needs $CODEX_MACHINE_CRED / -cred / -cred-file (run `codexbridge activate` first); self-host needs $CODEX_AGENT_KEY / -agent-key / -agent-key-file"))
	}
```

2. `enableE2EE(ctx, srv)`（L387）之后、`Initialize`（L391）之前插入：

```go
	// LAN direct listener (default ON): the same Server — token gate and every
	// clamp included. CODEX_LAN_ADDR overrides (":8767" form), "off" disables.
	// Starts BEFORE the license-credential wait below so an unactivated install
	// is immediately usable on the local network (the free tier).
	lanAddr := os.Getenv("CODEX_LAN_ADDR")
	if lanAddr == "" {
		lanAddr = ":8767"
	}
	if port := bridge.StartLANListener(srv, lanAddr); port != "" {
		srv.SetLANPort(port)
	}
	srv.SetPubEnabled(agentKey != "" || cred != "")
```

3. `Initialize` 成功之后、`srv.RunAgent(...)`（L408）之前插入后移的等待块：

```go
	if agentKey == "" && cred == "" {
		// License mode, not yet activated: the LAN listener above already
		// serves phones on the local network; wait for the tray activation
		// (「激活订阅码…」) to write the credential, then register with the hub.
		log.Printf("agent: not activated yet — LAN direct available; waiting for credential file %s (tray ▸ 激活订阅码)", credFile)
		for cred == "" {
			time.Sleep(5 * time.Second)
			cred = readSecretFile(credFile)
		}
		log.Printf("agent: credential found — registering")
		srv.SetPubEnabled(true)
	}
```

- [ ] **Step 2: runServe 标记 pub=false**

`runServe` 里 `enableE2EE(ctx, srv)`（L241）后加一行（serve 模式无中继档；不设 lanPort——其 addr 本身就是用户配置的直连地址）：

```go
	srv.SetPubEnabled(false) // serve mode has no relay tier; lanInfo says so
```

- [ ] **Step 3: 编译 + 全量测试**

Run: `cd bridge && go build ./cmd/... && go test ./... && go vet ./...`
Expected: 全部 PASS

- [ ] **Step 4: 手动冒烟（可选但推荐）**

```bash
cd bridge && CODEX_MACHINE_TOKEN=devtok CODEX_AGENT_KEY=devkey \
  go run ./cmd/codexbridge agent -hub ws://127.0.0.1:1/agent -id smoke 2>&1 | head -5
```
Expected: 日志含 `lan: direct listener on [::]:8767`（hub 连接失败正常——只验证监听启动）；`curl -s http://127.0.0.1:8767/healthz` 返回 `ok`。

- [ ] **Step 5: Commit**

```bash
git add bridge/cmd/codexbridge/main.go
git commit -m "feat(bridge): agent 模式默认开 LAN 监听；未激活先服务局域网再等订阅码"
```

---

### Task 4: Go — 托盘 QR 附带 `lan=` / `pub=`（+ 菜单状态行）

**Files:**
- Modify: `bridge/cmd/codexbridge/../codexmenubar/main.go`（config L38-44、loadConfig L86-135、pairQR L374-392、onReady L159+）

- [ ] **Step 1: config 增加 lanAddr**

`config` struct（L38-44）加字段 `lanAddr string`；`loadConfig` 在 `c.bridge` 解析之后加：

```go
	c.lanAddr = kv["LAN_ADDR"]
	if c.lanAddr == "" {
		c.lanAddr = ":8767"
	}
```

- [ ] **Step 2: lanParams 助手 + 接入 pairQR**

文件内新增（import 增加 `"net"`、`"strings"` 与 `xbridge "github.com/yunyuchen/codex-remote/bridge/internal/bridge"`；若 `strings` 已引则只加缺的）：

```go
// lanParams returns the "&lan=...&pub=0|1" connect-string suffix: this
// machine's current private IPv4s on the LAN listener port (QR-time seed; the
// bridge refreshes via lanInfo afterwards) and whether the relay tier is
// active (subscription credential present, or self-host agent key).
func lanParams(c config) string {
	pub := "0"
	if c.activated() || c.agentKey != "" {
		pub = "1"
	}
	s := "&pub=" + pub
	if c.lanAddr != "off" {
		if _, port, err := net.SplitHostPort(c.lanAddr); err == nil && port != "" {
			if cands := xbridge.LANCandidates(port); len(cands) > 0 {
				s += "&lan=" + url.QueryEscape(strings.Join(cands, ","))
			}
		}
	}
	return s
}
```

`pairQR`（L374-392）三处 `return c.phoneURL(), false` 与 `u := c.phoneURL() + "&pid=..."` 统一改基底：函数开头加 `base := c.phoneURL() + lanParams(c)`，三处 `c.phoneURL()` 全部换成 `base`。

- [ ] **Step 3: 菜单状态行**

`onReady` 里现有状态菜单项之后加一个禁用项（位置跟随现有 systray.AddMenuItem 排布，读上下文放在状态项下一行）：

```go
	lanLabel := "局域网直连: 开 (" + cfg.lanAddr + ")"
	if cfg.lanAddr == "off" {
		lanLabel = "局域网直连: 关"
	}
	systray.AddMenuItem(lanLabel, "手机与电脑同一 WiFi 时不经过中继直接连接").Disable()
```

- [ ] **Step 4: 编译验证**

Run: `cd bridge && go build ./cmd/codexmenubar && go vet ./cmd/codexmenubar`
Expected: 编译通过

- [ ] **Step 5: Commit**

```bash
git add bridge/cmd/codexmenubar/main.go
git commit -m "feat(menubar): 配对二维码附带 LAN 候选与公网档标志"
```

---

### Task 5: Dart — Machine 模型新字段

**Files:**
- Modify: `app/lib/src/models/machine.dart`
- Test: `app/test/machine_test.dart`（新建）

- [ ] **Step 1: 写失败测试**

```dart
// app/test/machine_test.dart
import 'package:flutter_test/flutter_test.dart';
import 'package:codex_remote_app/src/models/machine.dart';

void main() {
  test('新字段序列化往返', () {
    const m = Machine(
      id: '1', label: 'Mac', token: 'tok',
      lanCandidates: ['192.168.1.5:8767'],
      pubEnabled: false,
      linkMode: 'lanOnly',
    );
    final back = Machine.fromJson(m.toJson());
    expect(back.lanCandidates, ['192.168.1.5:8767']);
    expect(back.pubEnabled, false);
    expect(back.linkMode, 'lanOnly');
  });

  test('旧 JSON 兼容：缺字段取默认', () {
    final m = Machine.fromJson({'id': '1', 'label': 'x', 'token': 't'});
    expect(m.lanCandidates, isEmpty);
    expect(m.pubEnabled, true); // 旧存档来自中继配对 → 公网可用
    expect(m.linkMode, 'auto');
  });

  test('copyWith 更新 lan 字段', () {
    const m = Machine(id: '1', label: 'x', token: 't');
    final n = m.copyWith(lanCandidates: ['10.0.0.3:8767'], pubEnabled: false, linkMode: 'relayOnly');
    expect(n.lanCandidates, ['10.0.0.3:8767']);
    expect(n.pubEnabled, false);
    expect(n.linkMode, 'relayOnly');
    expect(n.token, 't'); // 不变量保留
  });
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd app && flutter test test/machine_test.dart`
Expected: FAIL —— `lanCandidates` 等命名参数不存在

- [ ] **Step 3: 实现（machine.dart）**

类字段区（`requireDeviceAuth` 之后）加：

```dart
  /// Cached LAN direct-connect candidates ("ip:port"): seeded from the pairing
  /// QR's `lan=` param, refreshed by every `lanInfo` frame (empty list from a
  /// lanInfo CLEARS the cache — the bridge is authoritative).
  final List<String> lanCandidates;

  /// Whether this machine's relay (public) tier is active — QR `pub=` param /
  /// lanInfo. UX only (greys 仅公网); the hub enforces the tier server-side.
  final bool pubEnabled;

  /// User link-mode override: 'auto' (race LAN first, relay 300ms behind),
  /// 'lanOnly', or 'relayOnly'.
  final String linkMode;
```

构造函数加 `this.lanCandidates = const [], this.pubEnabled = true, this.linkMode = 'auto',`；

`copyWith` 加三个可选参数并透传：

```dart
  Machine copyWith({String? label, String? host, bool? requireDeviceAuth,
      List<String>? lanCandidates, bool? pubEnabled, String? linkMode}) => Machine(
        id: id,
        label: label ?? this.label,
        token: token,
        host: host ?? this.host,
        requireDeviceAuth: requireDeviceAuth ?? this.requireDeviceAuth,
        lanCandidates: lanCandidates ?? this.lanCandidates,
        pubEnabled: pubEnabled ?? this.pubEnabled,
        linkMode: linkMode ?? this.linkMode,
      );
```

`toJson` 加（保持稀疏存储风格）：

```dart
        if (lanCandidates.isNotEmpty) 'lan': lanCandidates,
        if (!pubEnabled) 'pub': 0,
        if (linkMode != 'auto') 'link': linkMode,
```

`fromJson` 加：

```dart
        lanCandidates: (j['lan'] as List?)?.whereType<String>().toList() ?? const [],
        pubEnabled: j['pub'] != 0,
        linkMode: (j['link'] as String?) ?? 'auto',
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd app && flutter test test/machine_test.dart`
Expected: PASS（3 个测试）

- [ ] **Step 5: Commit**

```bash
git add app/lib/src/models/machine.dart app/test/machine_test.dart
git commit -m "feat(app): Machine 增加 lanCandidates/pubEnabled/linkMode"
```

---

### Task 6: Dart — 连接串解析与 LAN URL 构造（config.dart）

**Files:**
- Modify: `app/lib/src/config.dart`（文件末尾追加）
- Test: `app/test/config_test.dart`（新建）

- [ ] **Step 1: 写失败测试**

```dart
// app/test/config_test.dart
import 'package:flutter_test/flutter_test.dart';
import 'package:codex_remote_app/src/config.dart';

void main() {
  const qr = 'ws://1.2.3.4:39001/ws?token=abc&lan=192.168.1.5%3A8767%2C10.0.0.3%3A8767&pub=0';

  test('lanFromConnectString 解析逗号列表', () {
    expect(lanFromConnectString(qr), ['192.168.1.5:8767', '10.0.0.3:8767']);
    expect(lanFromConnectString('ws://h/ws?token=t'), isEmpty); // 老 QR
    expect(lanFromConnectString('not a url %%%'), isEmpty);
  });

  test('pubFromConnectString 三态', () {
    expect(pubFromConnectString(qr), false);
    expect(pubFromConnectString('ws://h/ws?token=t&pub=1'), true);
    expect(pubFromConnectString('ws://h/ws?token=t'), isNull); // 老 QR
  });

  test('lanWsUrl 构造直连地址', () {
    expect(lanWsUrl(' tok ', '192.168.1.5:8767'),
        'ws://192.168.1.5:8767/ws?token=tok');
  });
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd app && flutter test test/config_test.dart`
Expected: FAIL —— 函数未定义

- [ ] **Step 3: 实现（config.dart 末尾追加）**

```dart
/// Build the direct-LAN ws URL for one candidate ("ip:port"). Always plain
/// ws:// — a LAN IP has no cert; the E2EE layer protects the payload.
String lanWsUrl(String token, String hostPort) =>
    'ws://$hostPort/ws?token=${token.trim()}';

/// LAN direct-connect candidates from a scanned connect string (the comma-
/// separated `lan=` param the tray adds). Empty for old QRs / parse failures.
List<String> lanFromConnectString(String s) {
  try {
    final v = Uri.parse(s.trim()).queryParameters['lan'];
    if (v == null || v.isEmpty) return const [];
    return v.split(',').map((e) => e.trim()).where((e) => e.isNotEmpty).toList();
  } catch (_) {
    return const [];
  }
}

/// Relay-tier flag from a connect string (`pub=0|1`). Null when absent (old
/// QR) — callers keep the Machine default (true).
bool? pubFromConnectString(String s) {
  try {
    final v = Uri.parse(s.trim()).queryParameters['pub'];
    if (v == null) return null;
    return v != '0';
  } catch (_) {
    return null;
  }
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd app && flutter test test/config_test.dart`
Expected: PASS（3 个测试）

- [ ] **Step 5: Commit**

```bash
git add app/lib/src/config.dart app/test/config_test.dart
git commit -m "feat(app): 连接串 lan=/pub= 解析 + LAN 直连 URL 构造"
```

---

### Task 7: Dart — ConnectionRacer（happy-eyeballs 竞速）

**Files:**
- Create: `app/lib/src/services/connection_racer.dart`
- Test: `app/test/connection_racer_test.dart`

- [ ] **Step 1: 写失败测试**

```dart
// app/test/connection_racer_test.dart
import 'dart:async';
import 'package:flutter_test/flutter_test.dart';
import 'package:codex_remote_app/src/services/connection_racer.dart';

void main() {
  // 测试用 T=String：dialer 返回 url 本身当作"连接"，closer 记录被关掉的败者。
  Future<String> ok(String url, [Duration d = Duration.zero]) =>
      Future.delayed(d, () => url);

  test('LAN 先胜：中继被 300ms 延迟，LAN 立即成功', () async {
    final closed = <String>[];
    final out = await raceConnect<String>(
      lanUrls: ['ws://lan/ws'],
      relayUrl: 'wss://relay/ws',
      dialer: (u) => u.contains('lan')
          ? ok(u)
          : ok(u, const Duration(milliseconds: 50)),
      closeLoser: (c) async => closed.add(c),
    );
    expect(out.direct, true);
    expect(out.url, 'ws://lan/ws');
    await Future<void>.delayed(const Duration(milliseconds: 400));
    expect(closed, isEmpty); // 中继 timer 被取消，从未拨出
  });

  test('LAN 全失败：中继不等满 300ms 立即起拨并胜出', () async {
    final sw = Stopwatch()..start();
    final out = await raceConnect<String>(
      lanUrls: ['ws://lan1/ws', 'ws://lan2/ws'],
      relayUrl: 'wss://relay/ws',
      dialer: (u) =>
          u.contains('lan') ? Future.error(StateError('refused')) : ok(u),
      closeLoser: (_) async {},
    );
    expect(out.direct, false);
    expect(sw.elapsedMilliseconds, lessThan(250)); // 没有干等 grace
  });

  test('败者被关闭：双方都成功，先到者胜', () async {
    final closed = <String>[];
    final out = await raceConnect<String>(
      lanUrls: ['ws://lan/ws'],
      relayUrl: 'wss://relay/ws',
      relayDelay: Duration.zero,
      dialer: (u) => u.contains('lan')
          ? ok(u, const Duration(milliseconds: 30))
          : ok(u),
      closeLoser: (c) async => closed.add(c),
    );
    expect(out.direct, false); // relayDelay=0 且中继更快
    await Future<void>.delayed(const Duration(milliseconds: 100));
    expect(closed, ['ws://lan/ws']); // 迟到的 LAN 连接被立即关闭
  });

  test('全部失败：抛出最后一个错误', () async {
    expect(
      raceConnect<String>(
        lanUrls: ['ws://lan/ws'],
        relayUrl: 'wss://relay/ws',
        dialer: (_) => Future.error(StateError('down')),
        closeLoser: (_) async {},
      ),
      throwsStateError,
    );
  });

  test('无任何候选：StateError', () {
    expect(
      raceConnect<String>(
          lanUrls: const [], relayUrl: null,
          dialer: (u) => ok(u), closeLoser: (_) async {}),
      throwsStateError,
    );
  });

  test('lanOnly（relayUrl=null）：只拨 LAN', () async {
    final dialed = <String>[];
    final out = await raceConnect<String>(
      lanUrls: ['ws://lan/ws'],
      relayUrl: null,
      dialer: (u) { dialed.add(u); return ok(u); },
      closeLoser: (_) async {},
    );
    expect(out.direct, true);
    expect(dialed, ['ws://lan/ws']);
  });
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd app && flutter test test/connection_racer_test.dart`
Expected: FAIL —— `connection_racer.dart` 不存在

- [ ] **Step 3: 实现**

```dart
// app/lib/src/services/connection_racer.dart
//
// Happy-eyeballs candidate racing for the bridge link: LAN candidates dial
// immediately, the relay starts [relayDelay] later (giving same-WiFi direct a
// head start), and the FIRST completed dial wins. Losers are closed via
// [closeLoser] before they ever carry a frame, so the E2EE codec only arms on
// the winner. Generic over the channel type T so tests run without sockets.
import 'dart:async';

/// How the race was won.
class RaceOutcome<T> {
  final T channel;
  final String url;
  final bool direct; // true = a LAN candidate won
  RaceOutcome(this.channel, this.url, this.direct);
}

Future<RaceOutcome<T>> raceConnect<T>({
  required List<String> lanUrls,
  required String? relayUrl,
  required Future<T> Function(String url) dialer,
  required Future<void> Function(T channel) closeLoser,
  Duration relayDelay = const Duration(milliseconds: 300),
  Duration perTryTimeout = const Duration(seconds: 8),
}) {
  if (lanUrls.isEmpty && relayUrl == null) {
    return Future.error(StateError('no candidates'));
  }
  final done = Completer<RaceOutcome<T>>();
  final total = lanUrls.length + (relayUrl != null ? 1 : 0);
  var failures = 0;
  var relayStarted = false;
  Object lastErr = StateError('no candidates');
  Timer? relayTimer;

  Future<void> attempt(String url, bool direct) async {
    try {
      final ch = await dialer(url).timeout(perTryTimeout);
      if (done.isCompleted) {
        await closeLoser(ch); // lost — close before any frame travels
        return;
      }
      relayTimer?.cancel(); // a winner means the delayed relay never dials
      done.complete(RaceOutcome(ch, url, direct));
    } catch (e) {
      lastErr = e;
      failures++;
      if (failures == lanUrls.length && relayUrl != null && !relayStarted) {
        // Every LAN candidate already failed — don't sit out the grace delay.
        relayTimer?.cancel();
        relayStarted = true;
        // ignore: discarded_futures
        attempt(relayUrl, false);
      } else if (failures == total && !done.isCompleted) {
        done.completeError(lastErr);
      }
    }
  }

  for (final u in lanUrls) {
    // ignore: discarded_futures
    attempt(u, true);
  }
  if (relayUrl != null) {
    if (lanUrls.isEmpty) {
      relayStarted = true;
      // ignore: discarded_futures
      attempt(relayUrl, false);
    } else {
      relayTimer = Timer(relayDelay, () {
        if (relayStarted || done.isCompleted) return;
        relayStarted = true;
        // ignore: discarded_futures
        attempt(relayUrl, false);
      });
    }
  }
  return done.future;
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd app && flutter test test/connection_racer_test.dart`
Expected: PASS（6 个测试）

- [ ] **Step 5: Commit**

```bash
git add app/lib/src/services/connection_racer.dart app/test/connection_racer_test.dart
git commit -m "feat(app): ConnectionRacer —— LAN/中继 happy-eyeballs 竞速"
```

---

### Task 8: Dart — BridgeClient 集成（connectMachine + 竞速 + lanInfo + transport）

**Files:**
- Modify: `app/lib/src/services/bridge_client.dart`（imports、状态字段 ~L160、_scheduleReconnect L163-178、connect L185-282、_dispatch L638-711）

说明：现有 `connect(wsUrl, ...)` 改名为私有 `_connectRaced`（签名换成候选列表），保留一个同名薄包装兼容 `--dart-define=WS_URL` 启动路径；所有真实调用方下一个任务切到 `connectMachine`。

- [ ] **Step 1: imports + 状态字段**

文件头部 import 区加（已有的不重复）：

```dart
import '../models/machine.dart';
import 'connection_racer.dart';
```

（`config.dart` 若未引入则加 `import '../config.dart';` —— `connectMachine` 要用 `wsUrlForToken`/`lanWsUrl`。）

`_reconnectAttempts` 字段（L161）后加：

```dart
  // LAN direct racing state: the active machine's candidate URLs kept for
  // reconnect re-racing, and which transport won (drives the 直连/中继 badge).
  List<String> _activeLanUrls = const [];
  String? _activeRelayUrl;
  bool directTransport = false;

  /// Fires when the bridge pushes lanInfo: (machineId, candidates, pub).
  /// Wired to MachineStore.updateLanInfo in RootScreen.
  void Function(String machineId, List<String> candidates, bool pub)? onLanInfo;
```

- [ ] **Step 2: connectMachine + 兼容包装**

`connect` 方法（L185）之前插入：

```dart
  /// Connect to a saved machine: build its LAN + relay candidate URLs from the
  /// cached lanInfo and the user's link mode, then race them (relay 300ms
  /// behind). The FIRST pairing connect (pairingId set) never races — the
  /// one-time pid must be consumed on exactly one link: LAN first with a short
  /// timeout, then relay (sequential, inside _connectRaced).
  Future<void> connectMachine(Machine m,
      {String? pairingId, String? enrollPubB64, String? deviceName}) {
    final lan = [for (final c in m.lanCandidates) lanWsUrl(m.token, c)];
    return _connectRaced(
      relayUrl: m.linkMode == 'lanOnly' ? null : wsUrlForToken(m.token, host: m.host),
      lanUrls: m.linkMode == 'relayOnly' ? const [] : lan,
      machineId: m.id,
      pairingId: pairingId,
      enrollPubB64: enrollPubB64,
      deviceName: deviceName,
      requireDeviceAuth: m.requireDeviceAuth,
    );
  }

  /// Legacy single-URL connect (kept for the --dart-define=WS_URL boot path).
  Future<void> connect(
    String wsUrl, {
    String? machineId,
    String? pairingId,
    String? enrollPubB64,
    String? deviceName,
    bool requireDeviceAuth = false,
  }) =>
      _connectRaced(
        relayUrl: wsUrl,
        lanUrls: const [],
        machineId: machineId,
        pairingId: pairingId,
        enrollPubB64: enrollPubB64,
        deviceName: deviceName,
        requireDeviceAuth: requireDeviceAuth,
      );
```

- [ ] **Step 3: 原 connect 体改造为 _connectRaced**

原 `Future<void> connect(String wsUrl, {...}) async {` 签名行（L185-192）替换为：

```dart
  Future<void> _connectRaced({
    required String? relayUrl,
    required List<String> lanUrls,
    String? machineId,
    String? pairingId,
    String? enrollPubB64,
    String? deviceName,
    bool requireDeviceAuth = false,
  }) async {
    if (relayUrl == null && lanUrls.isEmpty) {
      // lanOnly with an empty candidate cache: nothing to dial.
      state = ConnState.error;
      error = '仅局域网模式：还没有直连地址（先在同一 WiFi 连一次，或改回自动）';
      notifyListeners();
      return;
    }
    _activeRelayUrl = relayUrl;
    _activeLanUrls = lanUrls;
```

体内两处适配：

1. `url = wsUrl;`（原 L209）改为 `url = relayUrl ?? lanUrls.first;`（占位，竞速后覆写为胜者）。
2. 单拨段（原 L241-251）：

```dart
      final useDeviceAuth = deviceSec != null && deviceAuthKeyId != null;
      // Device-auth rewriting applies ONLY to the relay URL — the hub runs the
      // challenge. Direct LAN always authenticates with the token query.
      final String? relayDial = relayUrl == null
          ? null
          : (useDeviceAuth ? _deviceAuthUrl(relayUrl, deviceAuthKeyId) : relayUrl);
      final RaceOutcome<WebSocketChannel> out;
      if (willPair) {
        out = await _pairingDial(lanUrls, relayDial);
      } else {
        out = await raceConnect<WebSocketChannel>(
          lanUrls: lanUrls,
          relayUrl: relayDial,
          dialer: _dialWs,
          closeLoser: (ch) => ch.sink.close(),
        );
      }
      final ch = out.channel;
      if (gen != _connectGen) {
        await ch.sink.close(); // superseded by a newer connect()
        return;
      }
      url = out.url;
      directTransport = out.direct;
      _ch = ch;
```

3. 原 `_awaitDeviceAuth = useDeviceAuth;`（L254）改为（hub 挑战只在中继路径胜出时发生）：

```dart
      _awaitDeviceAuth = useDeviceAuth && !out.direct;
```

（后面 L269 的 `else if (_awaitDeviceAuth)` 分支不用动——读的就是这个字段。）

末尾追加两个私有助手（`_deviceAuthUrl` 方法后）：

```dart
  /// Real WS dialer for the racer: connect + wait for the upgrade.
  Future<WebSocketChannel> _dialWs(String u) async {
    final ch = WebSocketChannel.connect(Uri.parse(u));
    await ch.ready;
    return ch;
  }

  /// Pairing never races (the pid is single-use): try each LAN candidate
  /// sequentially with a short timeout, then fall back to the relay.
  Future<RaceOutcome<WebSocketChannel>> _pairingDial(
      List<String> lanUrls, String? relayUrl) async {
    for (final u in lanUrls) {
      try {
        return await raceConnect<WebSocketChannel>(
          lanUrls: [u],
          relayUrl: null,
          dialer: _dialWs,
          closeLoser: (ch) => ch.sink.close(),
          perTryTimeout: const Duration(milliseconds: 1500),
        );
      } catch (_) {/* try the next candidate */}
    }
    if (relayUrl == null) throw StateError('no candidates');
    return raceConnect<WebSocketChannel>(
      lanUrls: const [],
      relayUrl: relayUrl,
      dialer: _dialWs,
      closeLoser: (ch) => ch.sink.close(),
    );
  }
```

- [ ] **Step 4: 重连改为重竞速**

`_scheduleReconnect`（L163-178）整体替换：

```dart
  void _scheduleReconnect() {
    if (suppressAutoConnect) return;
    if (_activeRelayUrl == null && _activeLanUrls.isEmpty) return;
    _reconnectTimer?.cancel();
    final ms = (500 * (1 << _reconnectAttempts)).clamp(500, 8000);
    if (_reconnectAttempts < 5) _reconnectAttempts++;
    _reconnectTimer = Timer(Duration(milliseconds: ms), () {
      if (!suppressAutoConnect && state != ConnState.connected) {
        // Re-RACE every time: coming home flips to direct, leaving falls back
        // to relay — and the PSK re-arms / device-auth re-runs as before.
        _connectRaced(
            relayUrl: _activeRelayUrl,
            lanUrls: _activeLanUrls,
            machineId: _activeMachineId,
            requireDeviceAuth: _activeRequireDeviceAuth);
      }
    });
  }
```

- [ ] **Step 5: _dispatch 处理 lanInfo**

`_dispatch` 的 switch（L638）加分支（`case 'error':` 之前）：

```dart
      case 'lanInfo':
        final mid = _activeMachineId;
        if (mid != null) {
          final cands = ((m['candidates'] as List?) ?? const [])
              .whereType<String>()
              .toList();
          onLanInfo?.call(mid, cands, m['pub'] != false);
        }
        break;
```

- [ ] **Step 6: 编译 + 既有测试不回归**

Run: `cd app && flutter analyze lib/src/services/bridge_client.dart && flutter test`
Expected: analyze 无 error；全部既有测试 PASS

- [ ] **Step 7: Commit**

```bash
git add app/lib/src/services/bridge_client.dart
git commit -m "feat(app): BridgeClient 候选竞速连接 + lanInfo 处理 + transport 状态"
```

---

### Task 9: Dart — MachineStore 更新方法 + 全部调用方切换

**Files:**
- Modify: `app/lib/src/services/machine_store.dart`（add L35-69、新方法）
- Modify: `app/lib/main.dart`（initState L57-61、didChangeAppLifecycleState L70-84、_boot L86-116）
- Modify: `app/lib/src/ui/sessions_screen.dart`（_switch L104-115、_retry L117-123、_removeMachine L728-742）
- Modify: `app/lib/src/ui/connect_screen.dart`（_connect L37-60）

- [ ] **Step 1: MachineStore 新方法（machine_store.dart 末尾 _persist 之前）**

```dart
  /// Refresh a machine's LAN candidates + relay-tier flag from a lanInfo
  /// frame. The frame is authoritative: an empty list CLEARS the cache.
  Future<void> updateLanInfo(String id, List<String> cands, bool pub) async {
    var changed = false;
    machines = machines.map((m) {
      if (m.id != id) return m;
      if (listEquals(m.lanCandidates, cands) && m.pubEnabled == pub) return m;
      changed = true;
      return m.copyWith(lanCandidates: cands, pubEnabled: pub);
    }).toList();
    if (changed) await _persist();
  }

  Future<void> setLinkMode(String id, String mode) async {
    machines = machines
        .map((m) => m.id == id ? m.copyWith(linkMode: mode) : m)
        .toList();
    await _persist();
  }
```

（`listEquals` 来自已 import 的 `package:flutter/foundation.dart`。）

`add`（L35）签名加 `List<String> lanCandidates = const [], bool? pubEnabled,`；重复 token 的 re-pair 分支（L44-53）把 copyWith 扩为：

```dart
        machines = machines
            .map((m) => m.id == dup.first.id
                ? m.copyWith(
                    host: h.isNotEmpty ? h : null,
                    requireDeviceAuth: requireDeviceAuth,
                    lanCandidates: lanCandidates.isNotEmpty ? lanCandidates : null,
                    pubEnabled: pubEnabled)
                : m)
            .toList();
```

（条件判断 `if ((h.isNotEmpty && ...))` 一并放宽为 re-pair 总是走该分支：直接去掉外层 if，保留 map。）新建 Machine 字面量（L58-64）加 `lanCandidates: lanCandidates, pubEnabled: pubEnabled ?? true,`。

- [ ] **Step 2: main.dart 切换**

`_RootScreenState.initState`（L57-61）加回调接线：

```dart
  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    bridge.onLanInfo = (id, cands, pub) => machines.updateLanInfo(id, cands, pub);
    _boot();
  }
```

`didChangeAppLifecycleState` 的连接调用（L81-82）换成：

```dart
      bridge.connectMachine(a);
```

`_boot` 的连接调用（L112-113）换成：

```dart
      await bridge.connectMachine(a);
```

（L92-108 的 `--dart-define` 种子路径不动——它走 `machines.add` 后同样落入 `connectMachine`。）

- [ ] **Step 3: sessions_screen.dart 切换**

`_switch`（L113-114）、`_retry`（L120-121）、`_removeMachine`（L736-737）三处
`bridge.connect(wsUrlForToken(...), machineId: ..., requireDeviceAuth: ...)` 统一换成：

```dart
    await bridge.connectMachine(m);
```

（`_retry`/`_removeMachine` 里变量名是 `a`：`await bridge.connectMachine(a);`。`_retry` 非 async，改为 `bridge.connectMachine(a);` 不加 await。顶部 `wsUrlForToken` import 若因此闲置，按 analyzer 提示清理。）

- [ ] **Step 4: connect_screen.dart 捕获 lan/pub**

`_connect`（L37-60）中部替换：

```dart
    final raw = _scanned;
    final deviceAuth = raw != null && deviceAuthFromConnectString(raw);
    final lan = raw != null ? lanFromConnectString(raw) : const <String>[];
    final pub = raw != null ? pubFromConnectString(raw) : null;
    final m = await machines.add(
        label: _nameCtrl.text,
        token: token,
        host: host,
        requireDeviceAuth: deviceAuth,
        lanCandidates: lan,
        pubEnabled: pub);
    await bridge.connectMachine(
      m,
      pairingId: raw != null ? pairingIdFromConnectString(raw) : null,
      enrollPubB64: raw != null ? enrollPubFromConnectString(raw) : null,
      deviceName: m.label,
    );
```

- [ ] **Step 5: 编译 + 全量测试**

Run: `cd app && flutter analyze && flutter test`
Expected: analyze 无 error；全部测试 PASS

- [ ] **Step 6: Commit**

```bash
git add app/lib/main.dart app/lib/src/services/machine_store.dart app/lib/src/ui/sessions_screen.dart app/lib/src/ui/connect_screen.dart
git commit -m "feat(app): 全部连接入口切换 connectMachine；QR lan/pub 落库"
```

---

### Task 10: Dart — UI（直连/中继徽章 + 连接方式三档 + lanOnly 文案）

**Files:**
- Modify: `app/lib/src/ui/sessions_screen.dart`（_machineChip L196-225、_machineMenu L667-695、_offline ~L320-330）

- [ ] **Step 1: 机器 chip 徽章**

`_machineChip` 的 Row children 里 `Text(m.label, ...)` 之后加：

```dart
            if (active && bridge.state == ConnState.connected) ...[
              const SizedBox(width: 6),
              Container(
                padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
                decoration: BoxDecoration(
                  color: Colors.white24,
                  borderRadius: BorderRadius.circular(8),
                ),
                child: Text(bridge.directTransport ? '直连' : '中继',
                    style: const TextStyle(fontSize: 10, color: Colors.white)),
              ),
            ],
```

（active chip 底色是 `Cx.ink` 深色，white24/白字可读。）

- [ ] **Step 2: 机器菜单加「连接方式」**

`_machineMenu` 的 children 里 `_menuTile(..., '重命名', 'rename')` 之前加：

```dart
            _menuTile(ctx, Icons.swap_horiz_rounded,
                '连接方式：${_linkModeLabel(m.linkMode)}', 'link'),
```

分发处（L692-694）加 `if (v == 'link') _pickLinkMode(m);`。

类内新增：

```dart
  String _linkModeLabel(String mode) => switch (mode) {
        'lanOnly' => '仅局域网',
        'relayOnly' => '仅公网',
        _ => '自动',
      };

  Future<void> _pickLinkMode(Machine m) async {
    final v = await showModalBottomSheet<String>(
      context: context,
      backgroundColor: Cx.bg,
      shape: const RoundedRectangleBorder(
          borderRadius: BorderRadius.vertical(top: Radius.circular(18))),
      builder: (ctx) => SafeArea(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            const Padding(
              padding: EdgeInsets.fromLTRB(20, 14, 20, 6),
              child: Text('连接方式',
                  style: TextStyle(
                      color: Cx.textSecondary,
                      fontSize: 13,
                      fontWeight: FontWeight.w600)),
            ),
            _menuTile(ctx, Icons.autorenew_rounded, '自动（推荐）', 'auto'),
            _menuTile(ctx, Icons.wifi_rounded, '仅局域网', 'lanOnly'),
            if (m.pubEnabled)
              _menuTile(ctx, Icons.public_rounded, '仅公网', 'relayOnly')
            else
              const ListTile(
                leading: Icon(Icons.public_off_rounded, color: Cx.textFaint),
                title: Text('仅公网（订阅后可用）',
                    style: TextStyle(color: Cx.textFaint)),
              ),
          ],
        ),
      ),
    );
    if (!mounted || v == null) return;
    await machines.setLinkMode(m.id, v);
    final updated = machines.machines.firstWhere((x) => x.id == m.id);
    if (m.id == machines.activeId) await bridge.connectMachine(updated);
  }
```

- [ ] **Step 3: lanOnly 离线文案**

`_offline()` 里 `Text('「$label」未连接', ...)` 之后加：

```dart
            if (machines.active?.linkMode == 'lanOnly') ...[
              const SizedBox(height: 6),
              const Text('仅局域网模式：确认手机和电脑在同一 WiFi',
                  style: TextStyle(color: Cx.textFaint, fontSize: 12)),
            ],
```

（插入位置以 `_offline` 实际 Column children 为准，跟随现有缩进。）

- [ ] **Step 4: 编译 + 全量测试**

Run: `cd app && flutter analyze && flutter test`
Expected: 无 error，全 PASS

- [ ] **Step 5: Commit**

```bash
git add app/lib/src/ui/sessions_screen.dart
git commit -m "feat(app): 直连/中继徽章 + 连接方式三档 + lanOnly 提示"
```

---

### Task 11: iOS 本地网络权限 + 文档同步

**Files:**
- Modify: `app/ios/Runner/Info.plist`
- Modify: `CLAUDE.md`（env 变量清单行）
- Verify: `miniprogram/utils/relay.js`（只读确认）

- [ ] **Step 1: Info.plist 加本地网络描述**

在 `<key>NSAppTransportSecurity</key>`（L60）的同级位置（其 `<dict>` 块之后）加：

```xml
	<key>NSLocalNetworkUsageDescription</key>
	<string>用于在同一 WiFi 下直连你的电脑，无需经过中继服务器。</string>
```

- [ ] **Step 2: CLAUDE.md env 清单**

「Relevant env vars」段落的 bridge 变量列表里加 `CODEX_LAN_ADDR`（默认 `:8767`，`off` 关闭 agent 模式的 LAN 直连监听）。

- [ ] **Step 3: 确认小程序安全忽略 lanInfo**

Run: `grep -n "case 'lanInfo'" miniprogram/utils/relay.js; sed -n '148,160p' miniprogram/utils/relay.js`
Expected: 无 lanInfo 分支；`_onMessage` 的 switch 无 default → 未知帧静默忽略（已人工确认，此步只是留痕）。无需改动。

- [ ] **Step 4: Commit**

```bash
git add app/ios/Runner/Info.plist CLAUDE.md
git commit -m "chore: iOS 本地网络权限描述 + CODEX_LAN_ADDR 文档"
```

---

### Task 12: 全量验证 + 手动 E2E 清单

- [ ] **Step 1: 全量自动化**

```bash
cd bridge && go test ./... && go vet ./... && go build ./cmd/...
cd ../app && flutter analyze && flutter test
```
Expected: 全部 PASS / 无 error

- [ ] **Step 2: 手动 E2E（真机，按 spec 测试节）**

1. Mac 上重启 agent（带新二进制），托盘重新显示二维码 → 手机删除旧机器重新扫码配对。
2. **免费档闭环**：断开 Mac 外网（或停 hub），手机同 WiFi 扫码 → 配对成功、对话、看图（/file 直连）。徽章显示「直连」。
3. **激活后出门**：恢复外网，手机切蜂窝 → 自动走中继，徽章「中继」。
4. **回家自动切**：手机回 WiFi，杀后台重开（或等重连）→ 徽章回「直连」。
5. **三档锁定**：仅局域网 → 蜂窝下显示「仅局域网模式」提示；仅公网 → 同 WiFi 也走中继；pub=0 的机器「仅公网」置灰。
6. iOS 首次直连弹「本地网络」权限 → 拒绝后仍能经中继连上（auto 回落）。

- [ ] **Step 3: 完成处理**

实现全部完成、验证通过后，使用 superpowers:finishing-a-development-branch 技能决定合并方式。

---

## Self-Review 记录（写计划时已核）

- **Spec 覆盖**：LAN 监听默认开/可关（T1/T3）、lanInfo（T2）、QR 种子+pub（T4/T6/T9）、Machine 字段（T5）、竞速+配对不竞速+重连重竞速（T7/T8）、三档+徽章+置灰+文案（T9/T10）、iOS 权限（T11）、hub 零改动（无任务即无改动）、协议三端注释（T2/T11）、测试矩阵（各任务+T12）。spec 的「未激活先可用」由 T3 的 cred-wait 后移实现。
- **类型一致性**：`raceConnect<T>`/`RaceOutcome<T>`（T7 定义、T8 以 `T=WebSocketChannel` 使用）；`Server.lanCands func(string)[]string`（T2 定义、T1 的 `LANCandidates` 同签名）；`Machine.copyWith` 新参数（T5 定义、T9 使用）一致。
- **无占位符**：每步均含完整代码/命令/预期输出。
