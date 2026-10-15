//go:build !linux

package app

// capFileSize: only Linux (and Android) guests run X servers.
func capFileSize(pid int, n int64) error { return nil }
