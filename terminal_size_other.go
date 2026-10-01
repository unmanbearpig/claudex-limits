//go:build !linux && !darwin

package main

import (
	"io"
	"os"
)

func notifyTerminalResize(chan<- os.Signal) {}

func terminalSizeForWriter(io.Writer) (int, int, bool) { return 0, 0, false }
