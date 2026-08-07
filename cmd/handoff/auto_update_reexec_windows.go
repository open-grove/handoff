//go:build windows

package main

import "errors"

func reexecUpdatedCLI(string, []string, []string) error {
	return errors.New("automatic replacement is not yet supported on Windows")
}
