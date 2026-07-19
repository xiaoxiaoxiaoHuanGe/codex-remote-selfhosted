//go:build !darwin && !windows

package provision

import "fmt"

// ResetMachineToken is only implemented for macOS and Windows agents.
func ResetMachineToken() (ResetResult, error) {
	return ResetResult{}, fmt.Errorf("reset-token is only supported on macOS and Windows")
}
