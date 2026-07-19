//go:build !darwin && !windows

package provision

// platformUUID: no portable stable id on other platforms; activation then
// simply skips the reinstall-reuse fast path.
func platformUUID() string { return "" }
