//go:build !linux

package platform

// diskSpace isn't measured off Linux (the platform runs on Linux; this is
// for building and testing elsewhere).
func diskSpace(path string) (total, free uint64) { return 0, 0 }
