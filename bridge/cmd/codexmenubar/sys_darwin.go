//go:build darwin

package main

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

//go:embed icon_on.png
var iconOn []byte

//go:embed icon_off.png
var iconOff []byte

func defaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex-remote", "menubar.env")
}

const alreadyRunningMsg = "Caret 已在运行 —— 图标在屏幕右上角的菜单栏。"

// trayLock pins the single-instance flock for the process lifetime.
var trayLock *os.File

// ensureSingleInstance guards against a second tray — Caret.app double-clicked
// in Finder while the launchd job already runs one — showing a confusing
// duplicate menu-bar icon. An flock under ~/.codex-remote detects the running
// instance; the lock dies with the process, so a crash can't wedge it.
func ensureSingleInstance() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return true // can't tell — let it run
	}
	dir := filepath.Join(home, ".codex-remote")
	_ = os.MkdirAll(dir, 0o700)
	f, err := os.OpenFile(filepath.Join(dir, "tray.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return false
	}
	trayLock = f
	return true
}

func clipboardCopy(s string) {
	cmd := exec.Command("pbcopy")
	in, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}
	_, _ = in.Write([]byte(s))
	_ = in.Close()
	_ = cmd.Wait()
}

func openPath(p string) { _ = exec.Command("open", p).Start() }

// notify shows a native banner; failures are non-fatal (e.g. notifications off).
func notify(msg string) {
	_ = exec.Command("osascript", "-e",
		fmt.Sprintf("display notification %q with title \"Codex Remote\"", msg)).Start()
}

// agentLabels are the launchd labels an agent may run under: the fixed label the
// .pkg installs, and the legacy per-machine label from install-agent-mac.sh.
func agentLabels(machineID string) []string {
	ls := []string{"com.example.caret.agent"}
	if machineID != "" {
		ls = append(ls, "com.example.codexagent."+machineID)
	}
	return ls
}

func restartAgent(machineID string) {
	for _, l := range agentLabels(machineID) {
		_ = exec.Command("launchctl", "kickstart", "-k",
			fmt.Sprintf("gui/%d/%s", os.Getuid(), l)).Start()
	}
}

// stopAgent unloads the agent's launchd job so the background agent stops with
// the tray. It is auto-loaded again at next login (it lives in ~/Library/LaunchAgents).
func stopAgent(machineID string) {
	for _, l := range agentLabels(machineID) {
		_ = exec.Command("launchctl", "bootout",
			fmt.Sprintf("gui/%d/%s", os.Getuid(), l)).Run()
	}
}

func agentLoaded(machineID string) bool {
	out, _ := exec.Command("launchctl", "list").Output()
	for _, l := range agentLabels(machineID) {
		if strings.Contains(string(out), l) {
			return true
		}
	}
	return false
}

// defaultBridgePath is where the .pkg installs codexbridge (BRIDGE= overrides).
func defaultBridgePath() string { return "/usr/local/codex-remote/codexbridge" }

// restartDesktopCodex gracefully restarts the desktop Codex GUI. The GUI only
// scans the (shared) ~/.codex session store at launch, so phone-driven turns
// stay invisible until it relaunches — a restart IS the sync.
func restartDesktopCodex() error {
	if exec.Command("pgrep", "-xq", "Codex").Run() != nil {
		return errors.New("Codex 桌面端未在运行")
	}
	_ = exec.Command("osascript", "-e", `quit app "Codex"`).Run()
	for i := 0; i < 20; i++ { // wait ≤5s for the graceful quit to finish
		if exec.Command("pgrep", "-xq", "Codex").Run() != nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	return exec.Command("open", "-a", "Codex").Run()
}

// promptInput shows a native text-input dialog; ok=false when the user cancels.
func promptInput(title, prompt string) (string, bool) {
	script := fmt.Sprintf(
		"text returned of (display dialog %q default answer \"\" with title %q buttons {\"取消\",\"确定\"} default button \"确定\")",
		prompt, title)
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return "", false // cancelled (osascript exits 1) or scripting unavailable
	}
	return strings.TrimSpace(string(out)), true
}

// alert shows a blocking message dialog (used where a notification is too easy
// to miss, e.g. activation results).
func alert(title, msg string) {
	_ = exec.Command("osascript", "-e",
		fmt.Sprintf("display dialog %q with title %q buttons {\"好\"} default button 1", msg, title)).Run()
}

// runHidden runs a CLI and returns its combined output (no special hiding
// needed on macOS — there is no console window to flash).
func runHidden(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

func openLog(machineID string) {
	home, _ := os.UserHomeDir()
	_ = exec.Command("open", filepath.Join(home, "Library", "Logs", "codex-remote")).Start()
}
