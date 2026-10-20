//go:build !linux && !android

package ui

import "syscall"

// dupTo makes newfd a copy of oldfd.
func dupTo(oldfd, newfd int) error { return syscall.Dup2(oldfd, newfd) }
