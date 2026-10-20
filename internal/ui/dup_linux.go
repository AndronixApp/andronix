//go:build linux || android

package ui

import "syscall"

// dupTo makes newfd a copy of oldfd (arm64 has no dup2: dup3).
func dupTo(oldfd, newfd int) error { return syscall.Dup3(oldfd, newfd, 0) }
