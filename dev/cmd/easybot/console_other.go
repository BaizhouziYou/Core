//go:build !windows

package main

import "os"

func enableConsoleColors() bool {
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0 && os.Getenv("TERM") != "dumb"
}

func waitAfterFailure() {}
