package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func historySnapshot(value float64) *Snapshot {
	return &Snapshot{FiveHourRemainingPercent: floatPtr(value), Windows: []GenericWindow{
		{Name: "Other / 15m", RemainingPercent: floatPtr(value / 2), WindowSeconds: 900},
	}}
}

func TestHistorySurvivesReopenAndRetainsFourHours(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "history")
	store := &historyStore{directory: directory}
	now := time.Date(2026, 9, 22, 15, 30, 0, 0, time.UTC)
	for _, record := range []historyRecord{
		{At: now.Add(-6 * time.Hour), Snapshot: historySnapshot(99)},
		{At: now.Add(-4*time.Hour - time.Second), Snapshot: historySnapshot(98)},
		{At: now.Add(-4 * time.Hour), Snapshot: historySnapshot(90)},
		{At: now.Add(-time.Hour)}, // Preserve a missed refresh through restart.
		{At: now, Snapshot: historySnapshot(70)},
		{At: now.Add(-2 * time.Hour), Snapshot: historySnapshot(80)},
		{At: now.Add(time.Minute), Snapshot: historySnapshot(60)},
	} {
		if err := store.append(record); err != nil {
			t.Fatal(err)
		}
	}
	reopened := &historyStore{directory: directory}
	history, err := reopened.load(now)
	if err != nil || len(history) != 4 {
		t.Fatalf("reopened history = %#v, %v", history, err)
	}
	if history[0].Values["5h"] != 90 || history[1].Values["5h"] != 80 || len(history[2].Values) != 0 || history[3].Values["Other / 15m"] != 35 {
		t.Fatalf("history lost values, ordering, or a gap: %#v", history)
	}
	oldPath := filepath.Join(directory, now.Add(-6*time.Hour).Format(historyFileLayout))
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired history file remains: %v", err)
	}
}

func TestHistoryLoadsReadingsFromBeforeRename(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	store, err := newHistoryStore(NewFileSource(filepath.Join(cache, "account.json")))
	if err != nil {
		t.Fatal(err)
	}
	account := filepath.Base(store.directory)
	if store.directory != filepath.Join(cache, "claudex-limits", "history", account) {
		t.Fatalf("new history directory = %q", store.directory)
	}
	legacy := &historyStore{directory: filepath.Join(cache, "codex-limits", "history", account)}
	now := time.Now()
	for _, record := range []historyRecord{
		{At: now.Add(-4*time.Hour - time.Second), Snapshot: historySnapshot(99)},
		{At: now.Add(-2 * time.Hour), Snapshot: historySnapshot(90)},
		{At: now.Add(-time.Hour)},
	} {
		if err := legacy.append(record); err != nil {
			t.Fatal(err)
		}
	}
	history, err := store.load(now)
	if err != nil || len(history) != 2 || history[0].Values["5h"] != 90 || len(history[1].Values) != 0 {
		t.Fatalf("legacy history before first new reading = %#v, %v", history, err)
	}
	if err := store.append(historyRecord{At: now, Snapshot: historySnapshot(70)}); err != nil {
		t.Fatal(err)
	}
	history, err = store.load(now)
	if err != nil || len(history) != 3 || history[0].Values["5h"] != 90 || len(history[1].Values) != 0 || history[2].Values["5h"] != 70 {
		t.Fatalf("combined history after first new reading = %#v, %v", history, err)
	}
	history, err = store.load(now.Add(5 * time.Hour))
	if err != nil || len(history) != 0 {
		t.Fatalf("expired history = %#v, %v", history, err)
	}
}

func TestHistoryConcurrentWritersKeepEveryReading(t *testing.T) {
	directory := t.TempDir()
	now := time.Now()
	var writers sync.WaitGroup
	for i := 0; i < 32; i++ {
		writers.Add(1)
		go func(i int) {
			defer writers.Done()
			store := &historyStore{directory: directory}
			if err := store.append(historyRecord{At: now.Add(-time.Duration(i) * time.Second), Snapshot: historySnapshot(float64(i))}); err != nil {
				t.Errorf("append: %v", err)
			}
		}(i)
	}
	writers.Wait()
	history, err := (&historyStore{directory: directory}).load(now)
	if err != nil || len(history) != 32 {
		t.Fatalf("concurrent history has %d readings, error %v", len(history), err)
	}
	for i, sample := range history {
		if sample.Values["5h"] != float64(31-i) {
			t.Fatalf("reading %d = %#v", i, sample)
		}
	}
}

func TestHistoryRecoversFromInterruptedAppend(t *testing.T) {
	store := &historyStore{directory: t.TempDir()}
	now := time.Now()
	if err := store.append(historyRecord{At: now.Add(-time.Second), Snapshot: historySnapshot(90)}); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(store.directory, now.UTC().Format(historyFileLayout)), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := io.WriteString(file, `{"at":"unfinished`)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		t.Fatal(err)
	}
	if err := store.append(historyRecord{At: now, Snapshot: historySnapshot(80)}); err != nil {
		t.Fatal(err)
	}
	history, err := store.load(now)
	if err == nil || len(history) != 2 || history[1].Values["5h"] != 80 {
		t.Fatalf("recovered history = %#v, %v", history, err)
	}
}

type historyTestSource struct {
	snapshot Snapshot
	err      error
}

func (s *historyTestSource) Read() (Snapshot, error) { return s.snapshot, s.err }
func (s *historyTestSource) Close() error            { return nil }

func TestHistoryWriteFailureKeepsFetchedSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var warnings strings.Builder
	source := &recordingSource{
		Source: &historyTestSource{snapshot: *historySnapshot(75)},
		store:  &historyStore{directory: filepath.Join(path, "history")}, warnings: &warnings,
	}
	for i := 0; i < 2; i++ {
		snapshot, err := source.Read()
		if err != nil || snapshot.FiveHourRemainingPercent == nil || *snapshot.FiveHourRemainingPercent != 75 {
			t.Fatalf("snapshot discarded after history failure: %#v, %v", snapshot, err)
		}
	}
	if strings.Count(warnings.String(), "could not save history") != 1 {
		t.Fatalf("history warning = %q", warnings.String())
	}
}

func TestRecordingSourcePersistsFailedRefreshAsGap(t *testing.T) {
	store := &historyStore{directory: t.TempDir()}
	failure := errors.New("offline")
	source := &recordingSource{Source: &historyTestSource{err: failure}, store: store, warnings: io.Discard}
	if _, err := source.Read(); !errors.Is(err, failure) {
		t.Fatalf("read error = %v", err)
	}
	history, err := store.load(time.Now())
	if err != nil || len(history) != 1 || len(history[0].Values) != 0 {
		t.Fatalf("persisted gap = %#v, %v", history, err)
	}
}

func TestHistorySeparatesSourcesAndExcludesDemo(t *testing.T) {
	directory := t.TempDir()
	first, err := newHistoryStore(NewFileSource(filepath.Join(directory, "first.json")))
	if err != nil {
		t.Fatal(err)
	}
	second, err := newHistoryStore(NewFileSource(filepath.Join(directory, "second.json")))
	if err != nil {
		t.Fatal(err)
	}
	if first.directory == second.directory {
		t.Fatal("independent OAuth sources share history")
	}
	demo := NewDemoSource()
	if withHistory(demo, io.Discard) != demo {
		t.Fatal("demo uses persisted history")
	}
}

// Run the actual CLI in another process so successive calls share only files,
// not memory, and --json output and signal handling are exercised as shipped.
func TestHistoryCLIHelper(t *testing.T) {
	if os.Getenv("CLAUDEX_LIMITS_TEST_CLI") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"claudex-limits"}, os.Args[i+1:]...)
			os.Exit(run())
		}
	}
	t.Fatal("missing CLI arguments")
}

func TestCLIAllModesPersistAndLiveRestartsRestoreGraph(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", directory)
	t.Setenv("CODEX_HOME", filepath.Join(directory, "codex"))
	script := filepath.Join(directory, "fake-codex")
	program := `#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s\n' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*) printf '{"id":%s,"result":{}}\n' "$id";;
    *'"method":"initialized"'*) :;;
    *'"method":"account/read"'*) printf '{"id":%s,"result":{"account":{"type":"chatgpt"}}}\n' "$id";;
    *'"method":"account/rateLimits/read"'*) printf '{"id":%s,"result":{"rateLimits":{"primary":{"usedPercent":25,"windowDurationMins":%s}}}}\n' "$id" "$CLAUDEX_LIMITS_TEST_WINDOW";;
    *) exit 9;;
  esac
done`
	if err := os.WriteFile(script, []byte(program), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDEX_LIMITS_CODEX_BIN", script)
	call := func(window string, args ...string) *exec.Cmd {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t.Cleanup(cancel)
		cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestHistoryCLIHelper$", "--", "--source", "codex"}, args...)...)
		cmd.Env = append(os.Environ(), "CLAUDEX_LIMITS_TEST_CLI=1", "CLAUDEX_LIMITS_TEST_WINDOW="+window)
		return cmd
	}
	if output, err := call("15").CombinedOutput(); err != nil || !strings.Contains(string(output), "15m left") {
		t.Fatalf("normal CLI call: %s, %v", output, err)
	}
	output, err := call("30", "--json").Output()
	var snapshot Snapshot
	if err != nil || json.Unmarshal(output, &snapshot) != nil || len(snapshot.Windows) != 1 || snapshot.Windows[0].Name != "30m" {
		t.Fatalf("JSON CLI call: %s, %v", output, err)
	}
	store, err := newHistoryStore(&appServerSource{})
	if err != nil {
		t.Fatal(err)
	}
	assertCount := func(want int) {
		t.Helper()
		history, err := store.load(time.Now())
		if err != nil || len(history) != want {
			t.Fatalf("persisted %d readings, want %d: %v", len(history), want, err)
		}
	}
	assertCount(2)
	for restart := 0; restart < 2; restart++ {
		cmd := call("300", "--live", "--interval", "60")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		var frames strings.Builder
		scanner := bufio.NewScanner(stdout)
		count := 0
		for scanner.Scan() {
			fmt.Fprintln(&frames, scanner.Text())
			if strings.Contains(scanner.Text(), "Ctrl+C quit") {
				count++
				if count == 2 { // Saved frame, then one freshly fetched frame.
					break
				}
			}
		}
		signalErr := cmd.Process.Signal(os.Interrupt)
		waitErr := cmd.Wait()
		if signalErr != nil || waitErr != nil || count != 2 {
			t.Fatalf("live restart %d: signal %v, exit %v, frames %d, stderr %s", restart, signalErr, waitErr, count, &stderr)
		}
		for _, name := range []string{"15m", "30m", "5h"} {
			if !strings.Contains(frames.String(), "━━  "+name) {
				t.Fatalf("live restart %d lost series %q: %s", restart, name, &frames)
			}
		}
		if !hasBraille(frames.String()) {
			t.Fatal("restored history did not draw graph points")
		}
		assertCount(3 + restart)
	}
}
