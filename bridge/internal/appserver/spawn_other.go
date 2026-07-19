//go:build !windows

package appserver

import "os/exec"

// hideSpawnWindow is a no-op outside Windows — see spawn_windows.go.
func hideSpawnWindow(*exec.Cmd) {}
