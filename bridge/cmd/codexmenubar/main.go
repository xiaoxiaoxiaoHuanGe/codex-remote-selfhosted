// Command codexmenubar is a cross-platform menu-bar / system-tray companion for the
// Codex Remote agent. It shows whether this machine is online at the hub and lets
// you copy the phone connect string or pop a QR the phone scans to connect — no
// terminal needed.
//
// It does NOT run the bridge itself; the OS service/task (install-agent-*) keeps
// doing that. This is a lightweight status panel reading the same per-machine token.
// Config: a small KEY=VALUE file (MACHINE_ID, MACHINE_NAME, HUB, TOKEN, AGENT_KEY)
// at the platform path returned by defaultConfigPath() — macOS: ~/.codex-remote,
// Windows: %ProgramData%\codex-remote.
//
// Platform-specific bits (icons, clipboard, opening files, notifications, agent
// status/restart) live in sys_darwin.go / sys_windows.go.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/systray"
	qrcode "github.com/skip2/go-qrcode"

	xbridge "github.com/yunyuchen/codex-remote/bridge/internal/bridge"

	"github.com/yunyuchen/codex-remote/bridge/internal/devstore"
	"github.com/yunyuchen/codex-remote/bridge/internal/provision"
)

// config is the resolved per-machine identity the panel needs.
type config struct {
	machineID, machineName, hub, token, agentKey, host string
	// license mode: where the per-machine credential lives (CRED_FILE, default
	// <configdir>/machine-cred) and the codexbridge binary that runs `activate`
	// (BRIDGE, default per-OS install path).
	credFile, bridge string
	// lanAddr mirrors the agent's CODEX_LAN_ADDR (LAN_ADDR in menubar.env,
	// default :8767, "off" = LAN direct disabled) — drives the QR lan= seed
	// and the menu status line.
	lanAddr string
}

// activated reports whether the license credential exists (self-host installs
// have no CRED_FILE semantics — their agent uses the shared key instead).
func (c config) activated() bool {
	b, err := os.ReadFile(c.credFile)
	return err == nil && len(strings.TrimSpace(string(b))) > 0
}

// hostIsBareIP reports whether host (possibly "ip:port") is a bare IP literal. A
// bare IP has no TLS cert, so the relay there is reached over ws/http (not wss/https)
// — needed for a mainland high-port relay before a domain + cert is set up.
func hostIsBareIP(host string) bool {
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i] // strip :port
	}
	return net.ParseIP(h) != nil
}

func (c config) wsScheme() string {
	if hostIsBareIP(c.host) {
		return "ws"
	}
	return "wss"
}

func (c config) httpScheme() string {
	if hostIsBareIP(c.host) {
		return "http"
	}
	return "https"
}

func (c config) phoneURL() string { return c.wsScheme() + "://" + c.host + "/ws?token=" + c.token }

func (c config) statusURL() string {
	return c.httpScheme() + "://" + c.host + "/machines?key=" + url.QueryEscape(c.agentKey)
}

// loadConfig parses the platform menubar.env. A missing TOKEN is treated as
// "not configured" so the UI can guide the user instead of silently doing nothing.
func loadConfig() (config, error) {
	var c config
	path := defaultConfigPath()
	b, err := os.ReadFile(path)
	if err != nil {
		return c, fmt.Errorf("read %s: %w", path, err)
	}
	kv := map[string]string{}
	// strip a UTF-8 BOM defensively (a BOM would corrupt the first KEY)
	body := strings.TrimPrefix(string(b), "\ufeff")
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		kv[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	c.machineID = kv["MACHINE_ID"]
	c.machineName = kv["MACHINE_NAME"]
	if c.machineName == "" {
		c.machineName = c.machineID
	}
	c.hub = kv["HUB"]
	if c.hub == "" {
		c.hub = "wss://relay.example.com/agent"
	}
	c.token = kv["TOKEN"]
	c.agentKey = kv["AGENT_KEY"]
	c.credFile = kv["CRED_FILE"]
	if c.credFile == "" {
		c.credFile = filepath.Join(filepath.Dir(path), "machine-cred")
	}
	c.lanAddr = kv["LAN_ADDR"]
	if c.lanAddr == "" {
		c.lanAddr = ":8767"
	}
	c.bridge = kv["BRIDGE"]
	if c.bridge == "" {
		c.bridge = defaultBridgePath()
	}
	if u, err := url.Parse(c.hub); err == nil && u.Host != "" {
		c.host = u.Host
	} else {
		c.host = "relay.example.com"
	}
	if c.token == "" {
		return c, fmt.Errorf("TOKEN missing in %s", path)
	}
	return c, nil
}

var (
	cfg    config
	cfgErr error

	mStatus    *systray.MenuItem
	mActivate  *systray.MenuItem
	mQR        *systray.MenuItem
	mCopyURL   *systray.MenuItem
	mCopyTok   *systray.MenuItem
	mRestart   *systray.MenuItem
	mSyncCodex *systray.MenuItem
	mReset     *systray.MenuItem
	mLog       *systray.MenuItem
	mQuit      *systray.MenuItem

	activating   bool // single-flight guard for the activation dialog
	syncingCodex bool // single-flight guard for the desktop-Codex restart
)

func main() {
	if !ensureSingleInstance() {
		// Second copy — the desktop shortcut / Caret.app opened while the
		// at-login instance already runs. Point at the existing icon and exit
		// instead of showing a duplicate tray.
		alert("Caret", alreadyRunningMsg)
		return
	}
	cfg, cfgErr = loadConfig()
	systray.Run(onReady, func() {})
}

func onReady() {
	systray.SetIcon(iconOff)
	systray.SetTooltip("Codex Remote")

	title := "Codex Remote"
	if cfgErr == nil {
		title = "Codex Remote · " + cfg.machineName
	}
	mTitle := systray.AddMenuItem(title, "")
	mTitle.Disable()
	// Enabled (not greyed) so the status reads clearly; clicking it forces a refresh.
	mStatus = systray.AddMenuItem("检查中…", "点此刷新状态")
	mActivate = systray.AddMenuItem("激活订阅码…", "输入 crk_ 订阅码,绑定这台机器")
	lanLabel := "局域网直连: 开 (" + cfg.lanAddr + ")"
	if cfg.lanAddr == "off" {
		lanLabel = "局域网直连: 关"
	}
	systray.AddMenuItem(lanLabel, "手机与电脑同一 WiFi 时不经过中继直接连接").Disable()
	systray.AddSeparator()

	mQR = systray.AddMenuItem("显示二维码…", "手机扫码连接")
	mCopyURL = systray.AddMenuItem("复制连接串", "wss:// 连接地址")
	mCopyTok = systray.AddMenuItem("复制 Token", "仅 token")
	systray.AddSeparator()
	mRestart = systray.AddMenuItem("重启后台代理", "")
	// 桌面 Codex GUI 只在启动时扫描共享的 ~/.codex 会话存储,手机端写入的
	// 新对话要等它重启才可见 —— 这里把"唯一的同步方式"做成一键。
	mSyncCodex = systray.AddMenuItem("重启桌面 Codex(同步手机会话)",
		"桌面端只在启动时读会话,重启后才能看到手机端的新内容")
	mReset = systray.AddMenuItem("重置密钥…", "轮换本机 token，旧的(含已外发的)立即失效")
	mLog = systray.AddMenuItem("打开数据目录", "")
	systray.AddSeparator()
	mQuit = systray.AddMenuItem("退出并停止代理", "退出托盘并停止后台代理")

	if cfgErr != nil {
		mStatus.SetTitle("⚠️ 未配置")
		for _, m := range []*systray.MenuItem{mActivate, mQR, mCopyURL, mCopyTok, mRestart, mReset, mLog} {
			m.Disable()
		}
	}

	go handleClicks()
	if cfgErr == nil {
		go pollStatus()
		notify("已启动 · 看托盘里的绿色 >_ 图标")
	}
}

func handleClicks() {
	for {
		select {
		case <-mActivate.ClickedCh:
			if !activating {
				activating = true
				go func() { runActivation(); activating = false }()
			}
		case <-mQR.ClickedCh:
			showQR()
		case <-mCopyURL.ClickedCh:
			clipboardCopy(cfg.phoneURL())
			notify("已复制连接串")
		case <-mCopyTok.ClickedCh:
			clipboardCopy(cfg.token)
			notify("已复制 Token")
		case <-mRestart.ClickedCh:
			restartAgent(cfg.machineID)
			notify("已重启后台代理")
		case <-mSyncCodex.ClickedCh:
			if !syncingCodex {
				syncingCodex = true
				go func() {
					// alert (not notify) on failure: notify is a no-op on
					// Windows and "未在运行" must reach the user.
					if err := restartDesktopCodex(); err != nil {
						alert("Caret", "重启桌面 Codex 失败:"+err.Error())
					} else {
						notify("已重启桌面 Codex — 手机端的会话更新现在可见")
					}
					syncingCodex = false
				}()
			}
		case <-mReset.ClickedCh:
			res, err := provision.ResetMachineToken()
			if err != nil {
				notify("重置失败：" + err.Error())
				break
			}
			cfg.token = res.NewToken
			notify("密钥已重置，旧的已失效 · 弹出新二维码请重新扫码")
			showQR()
			go refreshStatus()
		case <-mStatus.ClickedCh:
			go refreshStatus()
		case <-mLog.ClickedCh:
			openLog(cfg.machineID)
		case <-mQuit.ClickedCh:
			stopAgent(cfg.machineID)
			systray.Quit()
			return
		}
	}
}

// runActivation asks for a subscription key (native input dialog), exchanges it
// for this machine's credential via `codexbridge activate`, and restarts the
// agent so it registers with the fresh credential immediately (the agent also
// picks a first-time credential up by polling, but a RE-activation reissues the
// credential and the running process would keep retrying with the stale one).
func runActivation() {
	key, ok := promptInput("Caret 激活", "输入内测订阅码(crk_ 开头,向作者申请):")
	if !ok {
		return
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	if !strings.HasPrefix(key, "crk_") {
		alert("Caret 激活", "订阅码应以 crk_ 开头,请检查后重试。")
		return
	}
	base := cfg.httpScheme() + "://" + cfg.host
	out, err := runHidden(cfg.bridge, "activate",
		"-hub", base, "-key", key, "-id", cfg.machineID, "-name", cfg.machineName,
		"-out", cfg.credFile)
	if err != nil {
		// surface the bridge's last line verbatim (it carries the hub's reason:
		// bad key / roster full / unreachable)
		lines := strings.Split(strings.TrimSpace(out), "\n")
		alert("激活失败", lines[len(lines)-1])
		return
	}
	restartAgent(cfg.machineID)
	alert("Caret", "✓ 已激活并启动后台代理。\n\n点「显示二维码…」用手机 App 扫码即可连接这台电脑。")
	go refreshStatus()
}

// pollStatus refreshes the online indicator every 10s. "Online" means the hub
// lists this machineId (authoritative: that's what phones can reach); if the hub
// is unreachable or no AGENT_KEY is set, it falls back to whether the OS service
// /task is loaded.
func pollStatus() {
	refreshStatus()
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for range t.C {
		refreshStatus()
	}
}

// refreshStatus does one online check and updates the icon, tooltip and the
// (colored, prominent) status line. A green/red emoji shows even though menu
// text is otherwise muted.
func refreshStatus() {
	if queryOnline() {
		systray.SetIcon(iconOn)
		systray.SetTooltip("Codex Remote · 在线 (" + cfg.machineID + ")")
		mStatus.SetTitle("🟢  在线 · " + cfg.machineID)
	} else if cfg.agentKey == "" && !cfg.activated() {
		// license-mode install that hasn't been activated yet: guide, don't alarm
		systray.SetIcon(iconOff)
		systray.SetTooltip("Codex Remote · 未激活 (" + cfg.machineID + ")")
		mStatus.SetTitle("🟠  未激活 · 点「激活订阅码…」")
	} else {
		systray.SetIcon(iconOff)
		systray.SetTooltip("Codex Remote · 离线 (" + cfg.machineID + ")")
		mStatus.SetTitle("🔴  离线 · " + cfg.machineID)
	}
}

func queryOnline() bool {
	if cfg.agentKey == "" {
		return agentLoaded(cfg.machineID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.statusURL(), nil)
	if err != nil {
		return agentLoaded(cfg.machineID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return agentLoaded(cfg.machineID) // network blip — trust local state
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return agentLoaded(cfg.machineID)
	}
	var machines []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&machines); err != nil {
		return false
	}
	for _, m := range machines {
		if m.ID == cfg.machineID {
			return true
		}
	}
	return false
}

// e2eeDir mirrors bridge.E2EEDir (duplicated as a few lines rather than importing
// the whole bridge package into this lightweight tray). The bridge process writes
// enroll.json + the registries here when CODEX_E2EE=1.
func e2eeDir() string {
	if d := os.Getenv("CODEX_E2EE_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "bridge")
}

// enrollPub reads the bridge enrollment PUBLIC key (base64). Empty => E2EE not
// active on this machine yet (no enroll.json), so the QR falls back to legacy.
func enrollPub(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "enroll.json"))
	if err != nil {
		return ""
	}
	var ef struct {
		Pub string `json:"pub"`
	}
	if json.Unmarshal(b, &ef) != nil {
		return ""
	}
	return ef.Pub
}

// pairQR returns the QR connect string and whether E2EE was used. When E2EE is
// active it mints a SINGLE-USE pairingId (written to the shared pending-pairings.json
// with a 5-minute TTL) and appends pid + the enrollment pubkey, so the phone can run
// the pairing handshake; otherwise it returns the plain token URL. Falling back to
// the legacy URL on any error keeps the QR usable rather than failing the menu click.
func pairQR(c config) (string, bool) {
	base := c.phoneURL() + lanParams(c)
	dir := e2eeDir()
	epub := enrollPub(dir)
	if dir == "" || epub == "" {
		return base, false
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return base, false
	}
	pid := hex.EncodeToString(b)
	pend := devstore.OpenPending(filepath.Join(dir, "pending-pairings.json"))
	if err := pend.AddPending(pid, 5*time.Minute, time.Now()); err != nil {
		notify("配对码写入失败，已回退普通二维码")
		return base, false
	}
	u := base + "&pid=" + url.QueryEscape(pid) + "&epub=" + url.QueryEscape(epub)
	return u, true
}

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

func showQR() {
	content, e2ee := pairQR(cfg)
	png, err := qrcode.Encode(content, qrcode.Medium, 512)
	if err != nil {
		notify("二维码生成失败")
		return
	}
	path := filepath.Join(os.TempDir(), "codex-remote-qr.png")
	if err := os.WriteFile(path, png, 0o600); err != nil {
		notify("二维码写入失败")
		return
	}
	if e2ee {
		notify("E2EE 配对码已生成（5 分钟内有效，单次使用）")
	}
	openPath(path)
}
