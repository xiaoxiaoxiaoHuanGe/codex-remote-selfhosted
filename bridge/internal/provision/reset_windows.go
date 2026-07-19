//go:build windows

package provision

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const taskName = "CodexRemoteAgent"

func codexRemoteDir() string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return filepath.Join(pd, "codex-remote")
}

func configPath() string { return filepath.Join(codexRemoteDir(), "menubar.env") }

// ResetMachineToken rotates this Windows machine's phone token. It rewrites the
// scheduled task's -token argument (the agent's runtime source) and menubar.env /
// machine-<id>.token, then restarts the task so the agent re-registers with the
// new token. The old token is thereby invalidated.
func ResetMachineToken() (ResetResult, error) {
	var res ResetResult
	cfgPath := configPath()
	kv, err := readEnvFile(cfgPath)
	if err != nil {
		return res, fmt.Errorf("read %s: %w", cfgPath, err)
	}
	id := kv["MACHINE_ID"]
	if id == "" {
		return res, fmt.Errorf("MACHINE_ID missing in %s", cfgPath)
	}
	tok, err := newToken()
	if err != nil {
		return res, err
	}

	// 1) agent runtime source: swap the -token argument in the scheduled task.
	ps := fmt.Sprintf(`$ErrorActionPreference='Stop';`+
		`$t=Get-ScheduledTask -TaskName '%s';`+
		`$a=$t.Actions[0];`+
		`$na=[regex]::Replace($a.Arguments,'(-token\s+)\S+','${1}%s');`+
		`$act=New-ScheduledTaskAction -Execute $a.Execute -Argument $na -WorkingDirectory $a.WorkingDirectory;`+
		`Set-ScheduledTask -TaskName '%s' -Action $act | Out-Null`, taskName, tok, taskName)
	if err := powershell(ps); err != nil {
		return res, fmt.Errorf("update scheduled task: %w", err)
	}
	// 2) tray QR source + stored token file.
	if err := setEnvKey(cfgPath, "TOKEN", tok); err != nil {
		return res, fmt.Errorf("update menubar.env: %w", err)
	}
	_ = os.WriteFile(filepath.Join(codexRemoteDir(), "machine-"+id+".token"), []byte(tok), 0o600)
	// 3) restart the task so the new argument takes effect.
	_ = hidden("schtasks", "/End", "/TN", taskName).Run()
	if err := hidden("schtasks", "/Run", "/TN", taskName).Run(); err != nil {
		return res, fmt.Errorf("restart task: %w", err)
	}

	res = ResetResult{MachineID: id, NewToken: tok, Host: hostFromHub(kv["HUB"])}
	return res, nil
}

func powershell(script string) error {
	return hidden("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Run()
}

// hidden builds a command that won't flash a console window. HideWindow alone
// is NOT enough: Windows 11 with Windows Terminal as the default host ignores
// STARTF_USESHOWWINDOW, so schtasks/powershell still flashed a terminal.
// CREATE_NO_WINDOW allocates no console at all; pipes still work.
func hidden(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	return c
}
