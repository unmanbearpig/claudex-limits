package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// AccountSnapshot keeps provider-specific fields and failures available in a
// combined JSON response. An unavailable provider never becomes 100% remaining.
type AccountSnapshot struct {
	Provider string    `json:"provider"`
	Snapshot *Snapshot `json:"snapshot,omitempty"`
	Error    string    `json:"error,omitempty"`
}

type namedSource struct {
	name   string
	source Source
}

type combinedSource struct {
	sources []namedSource
}

func (s *combinedSource) Provider() string { return "all" }
func (s *ClaudeSource) Provider() string   { return "claude" }

func (s *combinedSource) Close() error {
	var errs []error
	for _, account := range s.sources {
		errs = append(errs, account.source.Close())
	}
	return errors.Join(errs...)
}

func (s *combinedSource) Read() (Snapshot, error) {
	accounts := make([]AccountSnapshot, len(s.sources))
	var wg sync.WaitGroup
	for i, source := range s.sources {
		wg.Go(func() {
			snapshot, err := source.source.Read()
			accounts[i].Provider = source.name
			if err != nil {
				accounts[i].Error = sanitizeError(err.Error())
			} else {
				accounts[i].Snapshot = &snapshot
			}
		})
	}
	wg.Wait()
	result := Snapshot{Provider: "all", Accounts: accounts, Windows: []GenericWindow{}, AdditionalRateLimits: []AdditionalLimit{}}
	var failures []error
	for _, account := range accounts {
		if account.Snapshot == nil {
			failures = append(failures, fmt.Errorf("%s: %s", claudeBucketName(account.Provider), account.Error))
			continue
		}
		if account.Provider == "codex" {
			// Preserve the original top-level JSON fields for Codex consumers.
			result.FiveHourRemainingPercent, result.FiveHourReset = account.Snapshot.FiveHourRemainingPercent, account.Snapshot.FiveHourReset
			result.WeeklyRemainingPercent, result.WeeklyReset = account.Snapshot.WeeklyRemainingPercent, account.Snapshot.WeeklyReset
			result.AdditionalRateLimits, result.BankedResets, result.PlanType = account.Snapshot.AdditionalRateLimits, account.Snapshot.BankedResets, account.Snapshot.PlanType
		}
		for _, window := range account.Snapshot.Windows {
			window.Name = claudeBucketName(account.Provider) + " / " + window.Name
			result.Windows = append(result.Windows, window)
		}
	}
	if len(failures) == len(accounts) {
		return Snapshot{}, errors.Join(failures...)
	}
	return result, nil
}

// pacedSource lets the combined chart retain Codex's existing refresh interval
// while fetching Claude usage at most once a minute, including failed reads.
type pacedSource struct {
	Source
	mu       sync.Mutex
	nextRead time.Time
	snapshot Snapshot
	err      error
}

func (s *pacedSource) Read() (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Now().Before(s.nextRead) {
		return s.snapshot, s.err
	}
	s.snapshot, s.err = s.Source.Read()
	s.nextRead = time.Now().Add(time.Minute)
	return s.snapshot, s.err
}

type unavailableSource struct{ err error }

func (s *unavailableSource) Read() (Snapshot, error) { return Snapshot{}, s.err }
func (s *unavailableSource) Close() error            { return nil }

func hasClaudeLogin(source *ClaudeSource) bool {
	if contents, err := os.ReadFile(source.Path); err == nil {
		var raw map[string]any
		if json.Unmarshal(contents, &raw) == nil {
			if disabled, _ := raw["disabled"].(bool); disabled {
				return false
			}
			if oauth := objectField(raw, "claudeAiOauth"); oauth != nil {
				return stringField(oauth, "accessToken", "access_token") != ""
			}
			return stringField(raw, "access_token") != "" && stringField(raw, "type") == "claude"
		}
	}
	if runtime.GOOS == "darwin" && source.keychainService != "" {
		// Ask Claude Code whether a login exists before touching its Keychain item.
		// This is a local status command and does not start a model turn.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		contents, err := exec.CommandContext(ctx, "claude", "auth", "status").Output()
		if err == nil {
			var status struct {
				LoggedIn   bool   `json:"loggedIn"`
				AuthMethod string `json:"authMethod"`
			}
			if json.Unmarshal(contents, &status) == nil {
				return status.LoggedIn && !strings.Contains(strings.ToLower(status.AuthMethod), "api")
			}
		}
	}
	return false
}

func openAutoSource() (Source, error) {
	claude, err := OpenClaudeSource("")
	if err != nil {
		return nil, err
	}
	hasClaude := hasClaudeLogin(claude)
	if !hasClaude {
		_ = claude.Close()
	}
	codex, codexErr := openCodexSource("auto", "", false)
	if errors.Is(codexErr, errInterrupted) {
		_ = claude.Close()
		return nil, codexErr
	}
	if !hasClaude {
		var loginErr *LoginRequiredError
		if errors.As(codexErr, &loginErr) {
			return nil, &LoginRequiredError{Message: "no Codex or Claude subscription login is available; sign in with the Codex CLI or Claude Code"}
		}
		return codex, codexErr
	}
	if codexErr != nil {
		var loginErr *LoginRequiredError
		if errors.As(codexErr, &loginErr) {
			return claude, nil
		}
		codex = &unavailableSource{err: codexErr}
	}
	return &combinedSource{sources: []namedSource{
		{name: "codex", source: codex},
		{name: "claude", source: &pacedSource{Source: claude}},
	}}, nil
}
