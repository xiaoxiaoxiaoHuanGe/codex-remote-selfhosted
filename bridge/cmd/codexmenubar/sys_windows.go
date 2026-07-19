//go:build windows

package main

import (
	_ "embed"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

//go:embed icon_on.ico
var iconOn []byte

//go:embed icon_off.ico
var iconOff []byte

// taskName is the Scheduled Task that runs the agent (see install-agent-windows.ps1).
const taskName = "CodexRemoteAgent"

const alreadyRunningMsg = "Caret 已在运行 —— 图标在任务栏右下角的托盘区(可能折叠在 ^ 里)。"

// ensureSingleInstance guards against a second tray — the desktop shortcut
// double-clicked while the at-logon task already runs one — showing a
// confusing duplicate icon. A per-session named mutex detects the running
// instance; the handle is deliberately never closed, the OS releases it when
// the process exits (so a crash can't wedge the lock).
func ensureSingleInstance() bool {
	name, _ := windows.UTF16PtrFromString("CaretTrayMutex")
	h, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
		return false
	}
	return true
}

func codexRemoteDir() string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return filepath.Join(pd, "codex-remote")
}

func defaultConfigPath() string { return filepath.Join(codexRemoteDir(), "menubar.env") }

// hidden builds a command that won't show a console window. HideWindow
// (SW_HIDE) alone is NOT enough: Windows 11 with Windows Terminal as the
// default terminal ignores STARTF_USESHOWWINDOW, so schtasks/tasklist still
// popped a PowerShell-looking terminal (seen right after activation, when
// restartAgent runs schtasks twice). CREATE_NO_WINDOW allocates no console at
// all — nothing for any terminal host to show; pipes still work. Keep SW_HIDE
// as a belt-and-suspenders for legacy conhost. (guiPS must NOT take HideWindow
// — it would hide the dialog itself; see its comment.)
func hidden(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	return c
}

func clipboardCopy(s string) {
	c := hidden("clip")
	in, err := c.StdinPipe()
	if err != nil {
		return
	}
	if err := c.Start(); err != nil {
		return
	}
	_, _ = in.Write([]byte(s))
	_ = in.Close()
	_ = c.Wait()
}

func openPath(p string) { _ = hidden("cmd", "/c", "start", "", p).Start() }

// notify is a no-op on Windows (a native toast needs extra deps); actions still run.
func notify(msg string) {}

func restartAgent(machineID string) {
	_ = hidden("schtasks", "/End", "/TN", taskName).Run()
	_ = hidden("schtasks", "/Run", "/TN", taskName).Start()
}

// stopAgent ends the running agent task so the background agent stops with the
// tray. It starts again at next logon (the task's AtLogOn trigger).
func stopAgent(machineID string) {
	_ = hidden("schtasks", "/End", "/TN", taskName).Run()
}

func agentLoaded(machineID string) bool {
	// Language-neutral: is the bridge process running? (schtasks status text is
	// localized/GBK on zh-CN Windows and won't match a UTF-8 literal.)
	out, _ := hidden("tasklist", "/FI", "IMAGENAME eq codexbridge.exe", "/FO", "CSV", "/NH").Output()
	return strings.Contains(string(out), "codexbridge.exe")
}

// restartDesktopCodex restarts the desktop Codex GUI (Codex.exe). The GUI only
// scans the (shared) ~/.codex session store at launch, so phone-driven turns
// stay invisible until it relaunches — a restart IS the sync. The running
// process's path is captured first so the same install can be relaunched.
func restartDesktopCodex() error {
	out, _ := hidden("powershell", "-NoProfile", "-Command",
		`(Get-Process Codex -ErrorAction SilentlyContinue | Select-Object -First 1).Path`).Output()
	path := strings.TrimSpace(string(out))
	if path == "" {
		return errors.New("Codex 桌面端未在运行")
	}
	_ = hidden("taskkill", "/IM", "Codex.exe").Run() // graceful close first
	for i := 0; i < 10; i++ {                        // wait ≤5s for it to exit
		o, _ := hidden("tasklist", "/FI", "IMAGENAME eq Codex.exe", "/FO", "CSV", "/NH").Output()
		if !strings.Contains(string(o), "Codex.exe") {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	_ = hidden("taskkill", "/IM", "Codex.exe", "/F").Run() // stragglers
	return hidden("cmd", "/c", "start", "", path).Start()
}

// defaultBridgePath: the installer puts codexbridge.exe next to the tray exe
// (both in %ProgramData%\codex-remote); BRIDGE= in menubar.env overrides.
func defaultBridgePath() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "codexbridge.exe")
	}
	return filepath.Join(codexRemoteDir(), "codexbridge.exe")
}

// guiPS runs a PowerShell snippet that SHOWS GUI dialogs. Two traps it avoids:
//   - hidden()'s STARTF_USESHOWWINDOW/SW_HIDE makes the child's FIRST GUI window
//     (the dialog itself!) invisible — CREATE_NO_WINDOW suppresses only the
//     console and leaves dialogs alone.
//   - -Command with CJK garbles under some codepages — -EncodedCommand carries
//     the script as UTF-16LE base64, immune to encoding entirely.
func guiPS(script string) *exec.Cmd {
	u16 := utf16.Encode([]rune("[Console]::OutputEncoding=[System.Text.Encoding]::UTF8; " + script))
	raw := make([]byte, len(u16)*2)
	for i, v := range u16 {
		raw[i*2], raw[i*2+1] = byte(v), byte(v>>8)
	}
	c := exec.Command("powershell", "-NoProfile", "-EncodedCommand",
		base64.StdEncoding.EncodeToString(raw))
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return c
}

// promptInput shows a text-input dialog via the VB InputBox (no extra deps);
// ok=false when the user cancels (InputBox then returns an empty string).
func promptInput(title, prompt string) (string, bool) {
	ps := `Add-Type -AssemblyName Microsoft.VisualBasic; [Microsoft.VisualBasic.Interaction]::InputBox(` +
		psQuote(prompt) + `,` + psQuote(title) + `,'')`
	out, err := guiPS(ps).Output()
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(out))
	return s, s != ""
}

// alert shows a blocking message dialog (notify is a no-op on Windows, so
// activation results need a real dialog).
func alert(title, msg string) {
	ps := `Add-Type -AssemblyName PresentationFramework; [System.Windows.MessageBox]::Show(` +
		psQuote(msg) + `,` + psQuote(title) + `) | Out-Null`
	_ = guiPS(ps).Run()
}

// psQuote single-quotes a string for PowerShell (doubling embedded quotes).
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// runHidden runs a CLI without flashing a console window, returning combined output.
func runHidden(name string, args ...string) (string, error) {
	out, err := hidden(name, args...).CombinedOutput()
	return string(out), err
}

func openLog(machineID string) { openPath(codexRemoteDir()) }
