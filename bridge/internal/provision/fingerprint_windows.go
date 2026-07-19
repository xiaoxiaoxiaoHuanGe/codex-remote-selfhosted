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
