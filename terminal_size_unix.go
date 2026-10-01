//go:build linux || darwin

package main

import (
	"io"
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

type windowSize struct {
	rows    uint16
	columns uint16
	_       uint32
}

func notifyTerminalResize(ch chan<- os.Signal) {
	signal.Notify(ch, syscall.SIGWINCH)
}

func terminalSizeForWriter(w io.Writer) (int, int, bool) {
	file, ok := w.(*os.File)
	if !ok {
		return 0, 0, false
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return 0, 0, false
	}
	var size windowSize
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&size)))
	if errno != 0 || size.columns == 0 || size.rows == 0 {
		return 0, 0, false
	}
	return int(size.columns), int(size.rows), true
}
