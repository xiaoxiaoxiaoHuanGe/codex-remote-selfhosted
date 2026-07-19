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
