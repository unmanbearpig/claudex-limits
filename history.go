package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const historyFileLayout = "2006-01-02T15.jsonl"

// Each reading is appended separately so simultaneous CLI invocations never
// overwrite one another. Hourly files let us prune old data without rewriting
// a file another process might be appending to.
type historyStore struct {
	directory string
}

type historyRecord struct {
	At       time.Time `json:"at"`
	Snapshot *Snapshot `json:"snapshot"`
}

func newHistoryStore(source Source) (*historyStore, error) {
	directory, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	// Keep independently selected sources apart. Hash paths so history filenames
	// do not disclose OAuth filenames, which can contain email addresses.
	var key string
	switch source := source.(type) {
	case *FileSource:
		path, err := filepath.Abs(source.Path)
		if err != nil {
			return nil, err
		}
		key = "file:" + path
	case *appServerSource:
		home := strings.TrimSpace(os.Getenv("CODEX_HOME"))
		if home == "" {
			home = filepath.Join(homeDirectory(), ".codex")
		}
		path, err := filepath.Abs(home)
		if err != nil {
			return nil, err
		}
		key = "codex:" + path
	default:
		return nil, errors.New("history is unavailable for this source")
	}
	return &historyStore{directory: filepath.Join(directory, "codex-limits", "history", fmt.Sprintf("%x", sha256.Sum256([]byte(key))))}, nil
}

func (s *historyStore) append(record historyRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.directory, 0700); err != nil {
		return err
	}
	path := filepath.Join(s.directory, record.At.UTC().Format(historyFileLayout))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	// A leading newline isolates a partial record left by an interrupted write.
	// Write the entire record in one append to avoid interleaving writers.
	data = append(append([]byte{'\n'}, data...), '\n')
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	return s.prune(record.At)
}

func (s *historyStore) prune(now time.Time) error {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return err
	}
	cutoff := now.Add(-time.Duration(HistorySeconds) * time.Second)
	for _, entry := range entries {
		hour, err := time.Parse(historyFileLayout, entry.Name())
		if err != nil || entry.IsDir() || hour.Add(time.Hour).After(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(s.directory, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *historyStore) load(now time.Time) ([]Sample, error) {
	entries, err := os.ReadDir(s.directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cutoff := now.Add(-time.Duration(HistorySeconds) * time.Second)
	var history []Sample
	var loadErr error
	for _, entry := range entries {
		hour, err := time.Parse(historyFileLayout, entry.Name())
		if err != nil || entry.IsDir() || hour.Add(time.Hour).Before(cutoff) || hour.After(now) {
			continue
		}
		file, err := os.Open(filepath.Join(s.directory, entry.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue // Another invocation may have pruned this hour.
		}
		if err != nil {
			loadErr = err
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 4096), 8<<20)
		for scanner.Scan() {
			if len(scanner.Bytes()) == 0 {
				continue
			}
			var record historyRecord
			if err := json.Unmarshal(scanner.Bytes(), &record); err != nil || record.At.IsZero() {
				loadErr = errors.New("skipped damaged history records")
				continue
			}
			if record.At.Before(cutoff) || record.At.After(now) {
				continue
			}
			sample := Sample{At: record.At}
			if record.Snapshot != nil {
				sample.Values = snapshotValues(*record.Snapshot)
			}
			history = append(history, sample)
		}
		if err := scanner.Err(); err != nil {
			loadErr = err
		}
		if err := file.Close(); err != nil {
			loadErr = err
		}
	}
	// Concurrent processes may finish writing in a different order than they
	// fetched their readings. Chart drawing and age trimming require time order.
	sort.SliceStable(history, func(i, j int) bool { return history[i].At.Before(history[j].At) })
	return history, loadErr
}

type recordingSource struct {
	Source
	store    *historyStore
	warnings io.Writer
	warned   bool
}

func withHistory(source Source, warnings io.Writer) Source {
	if _, demo := source.(*demoSource); demo {
		return source
	}
	store, err := newHistoryStore(source)
	if err != nil {
		fmt.Fprintf(warnings, "codex-limits: history unavailable: %v\n", err)
		return source
	}
	return &recordingSource{Source: source, store: store, warnings: warnings}
}

func (s *recordingSource) Read() (Snapshot, error) {
	snapshot, err := s.Source.Read()
	record := historyRecord{At: time.Now()}
	if err == nil {
		record.Snapshot = &snapshot
	}
	if saveErr := s.store.append(record); saveErr != nil {
		if !s.warned {
			fmt.Fprintf(s.warnings, "codex-limits: could not save history: %v\n", saveErr)
			s.warned = true
		}
	} else {
		s.warned = false
	}
	return snapshot, err
}

func (s *recordingSource) History(now time.Time) []Sample {
	history, err := s.store.load(now)
	if err != nil {
		fmt.Fprintf(s.warnings, "codex-limits: could not load all history: %v\n", err)
	}
	return history
}
