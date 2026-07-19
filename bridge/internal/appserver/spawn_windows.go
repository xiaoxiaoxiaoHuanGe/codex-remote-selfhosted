//go:build windows

package appserver

import (
	"os/exec"
	"syscall"
)

// hideSpawnWindow keeps the spawned codex app-server from popping a console
// window. codexbridge.exe ships as a GUI-subsystem binary (-H windowsgui), so
// it has no console of its own — when it spawns the console-subsystem
// codex.exe, Windows allocates a brand-new VISIBLE console for the child: a
// black box that appears at every logon and stays. Users close it, which kills
// the app-server and with it the whole phone link. CREATE_NO_WINDOW allocates
// no console at all (stdio pipes are unaffected — we talk JSON-RPC over them);
// HideWindow (SW_HIDE) stays as belt-and-suspenders for legacy conhost, but is
// NOT sufficient alone: Windows 11 with Windows Terminal as default host
// ignores STARTF_USESHOWWINDOW.
func hideSpawnWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
