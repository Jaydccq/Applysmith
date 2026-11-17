//go:build !darwin && !linux

package workspace

// filesystemType is unknown on this platform, so the local-volume rule passes.
func filesystemType(string) string { return "" }
