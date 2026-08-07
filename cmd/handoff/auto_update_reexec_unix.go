//go:build !windows

package main

import "syscall"

func reexecUpdatedCLI(executable string, args, environment []string) error {
	return syscall.Exec(executable, append([]string{executable}, args...), environment)
}
