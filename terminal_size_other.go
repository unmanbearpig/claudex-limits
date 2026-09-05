//go:build !linux && !darwin

package main

import "io"

func terminalSizeForWriter(io.Writer) (int, int, bool) { return 0, 0, false }
