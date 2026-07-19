//go:build darwin

package provision

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex-remote", "menubar.env")
}

// ResetMachineToken rotates this Mac's phone token. It writes the new token into
// the launchd plist's EnvironmentVariables (the agent's runtime source) and into
// menubar.env (the tray's QR source), then reloads the launchd job so the agent
// re-registers with the new token. The old token is thereby invalidated.
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
	home, _ := os.UserHomeDir()
	plist := filepath.Join(home, "Library", "LaunchAgents", "com.example.codexagent."+id+".plist")
	if _, err := os.Stat(plist); err != nil {
		return res, fmt.Errorf("agent plist not found: %s", plist)
	}

	tok, err := newToken()
	if err != nil {
		return res, err
	}

	// 1) agent runtime source (plist env).
	if err := plistSet(plist, ":EnvironmentVariables:CODEX_MACHINE_TOKEN", tok); err != nil {
		return res, fmt.Errorf("update plist token: %w", err)
	}
	// 2) tray QR source.
	if err := setEnvKey(cfgPath, "TOKEN", tok); err != nil {
		return res, fmt.Errorf("update menubar.env: %w", err)
	}
	// 3) reload the job so the NEW plist env takes effect (kickstart alone reuses
	//    the cached env; bootout+bootstrap re-reads the plist from disk).
	label := fmt.Sprintf("com.example.codexagent.%s", id)
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", domain+"/"+label).Run() // ignore if not loaded
	if err := exec.Command("launchctl", "bootstrap", domain, plist).Run(); err != nil {
		// Already loaded (race) — fall back to a hard restart.
		if kerr := exec.Command("launchctl", "kickstart", "-k", domain+"/"+label).Run(); kerr != nil {
			return res, fmt.Errorf("reload agent: %w", err)
		}
	}

	res = ResetResult{MachineID: id, NewToken: tok, Host: hostFromHub(kv["HUB"])}
	return res, nil
}

// plistSet sets a value, falling back to Add if the key path doesn't exist yet.
func plistSet(plist, keyPath, val string) error {
	if err := exec.Command("/usr/libexec/PlistBuddy", "-c", "Set "+keyPath+" "+val, plist).Run(); err == nil {
		return nil
	}
	return exec.Command("/usr/libexec/PlistBuddy", "-c", "Add "+keyPath+" string "+val, plist).Run()
}
