package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

type resizeTestSource struct {
	mode      string
	reads     atomic.Int32
	stop      chan struct{}
	closeOnce sync.Once
}

func (s *resizeTestSource) History(now time.Time) []Sample {
	return []Sample{{At: now.Add(-5 * time.Second), Values: map[string]float64{"5h": 75}}}
}

func (s *resizeTestSource) Read() (Snapshot, error) {
	s.reads.Add(1)
	switch s.mode {
	case "blocked":
		<-s.stop
		return Snapshot{}, errors.New("closed")
	case "failed":
		return Snapshot{}, errors.New("offline")
	default:
		return Snapshot{FiveHourRemainingPercent: floatPtr(75)}, nil
	}
}

func (s *resizeTestSource) Close() error {
	s.closeOnce.Do(func() { close(s.stop) })
	return nil
}

func TestRunLiveResizeHelper(t *testing.T) {
	mode := os.Getenv("CLAUDEX_LIMITS_TEST_RESIZE")
	if mode == "" {
		return
	}
	source := &resizeTestSource{mode: mode, stop: make(chan struct{})}
	err := RunLive(os.Stdout, source, 60)
	_ = source.Close()
	fmt.Fprintf(os.Stderr, "reads=%d\n", source.reads.Load())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func testPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("PTY unavailable: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })
	var unlock int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		t.Fatal(errno)
	}
	var number uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&number))); errno != 0 {
		t.Fatal(errno)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	return master, slave
}

func resizeTestPTY(t *testing.T, terminal *os.File, columns, rows int) {
	t.Helper()
	size := windowSize{columns: uint16(columns), rows: uint16(rows)}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, terminal.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&size))); errno != 0 {
		t.Fatal(errno)
	}
}

func TestRunLiveRedrawsOnTerminalResizeWithoutFetching(t *testing.T) {
	controls := regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	for _, mode := range []string{"idle", "blocked", "failed"} {
		t.Run(mode, func(t *testing.T) {
			master, slave := testPTY(t)
			resizeTestPTY(t, master, 88, 24)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunLiveResizeHelper$")
			cmd.Env = append(os.Environ(), "CLAUDEX_LIMITS_TEST_RESIZE="+mode, "TERM=xterm-256color", "NO_COLOR=1")
			cmd.Stdin, cmd.Stdout = slave, slave
			// A controlling terminal makes the kernel deliver SIGWINCH when
			// TIOCSWINSZ changes the size, just as a terminal emulator does.
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			_ = slave.Close()
			frames := make(chan string, 8)
			go func() {
				defer close(frames)
				pending := ""
				buffer := make([]byte, 65536)
				for {
					n, err := master.Read(buffer)
					pending += string(buffer[:n])
					for {
						frame, rest, ok := strings.Cut(pending, "\x1b[J")
						if !ok {
							break
						}
						frames <- controls.ReplaceAllString(strings.ReplaceAll(frame, "\r", ""), "")
						pending = rest
					}
					if err != nil {
						return
					}
				}
			}()
			waitFrame := func(ready func(string) bool) string {
				t.Helper()
				deadline := time.NewTimer(2 * time.Second)
				defer deadline.Stop()
				for {
					select {
					case frame, ok := <-frames:
						if !ok {
							t.Fatal("live process exited before redrawing")
						}
						if ready(frame) {
							return frame
						}
					case <-deadline.C:
						t.Fatal("redraw waited for the 60-second quota refresh")
					}
				}
			}
			waitFrame(func(frame string) bool {
				switch mode {
				case "idle":
					return strings.Contains(frame, "75.0% left")
				case "failed":
					return strings.Contains(frame, "Refresh failed")
				default:
					return strings.Contains(frame, "Ctrl+C quit")
				}
			})
			var slowest time.Duration
			for _, size := range [][2]int{{40, 80}, {100, 30}, {36, 140}, {88, 24}} {
				started := time.Now()
				resizeTestPTY(t, master, size[0], size[1])
				frame := waitFrame(func(frame string) bool {
					return strings.Contains(frame, "Ctrl+C quit")
				})
				slowest = max(slowest, time.Since(started))
				lines := strings.Split(strings.TrimSuffix(frame, "\n"), "\n")
				if len(lines) != size[1]-1 {
					t.Fatalf("resized frame uses %d rows, want %d", len(lines), size[1]-1)
				}
				if len([]rune(lines[2])) != size[0]-1 {
					t.Fatalf("chart width did not follow resize to %d columns: %q", size[0], lines[2])
				}
				if mode == "idle" && !strings.Contains(frame, "75.0% left") {
					t.Fatal("resize lost the last successful values")
				}
				if mode == "failed" && !strings.Contains(frame, "Refresh failed") {
					t.Fatal("resize hid the refresh error")
				}
			}
			if err := cmd.Process.Signal(os.Interrupt); err != nil {
				t.Fatal(err)
			}
			err := cmd.Wait()
			waited = true
			if err != nil || stderr.String() != "reads=1\n" {
				t.Fatalf("resize triggered another fetch or failed to exit: %v, %s", err, &stderr)
			}
			t.Logf("slowest resize redraw: %s", slowest)
		})
	}
}
