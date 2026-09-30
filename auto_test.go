package main

import (
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type countedSource struct {
	Source
	reads int
}

func (s *countedSource) Read() (Snapshot, error) {
	s.reads++
	return s.Source.Read()
}

func TestCombinedSourceKeepsProviderFailuresSeparateAndPacesClaude(t *testing.T) {
	claude := &countedSource{Source: &historyTestSource{snapshot: NormalizeClaudeUsage(map[string]any{
		"five_hour": map[string]any{"utilization": 25},
	}, time.Now())}}
	codex := &historyTestSource{snapshot: Snapshot{FiveHourRemainingPercent: floatPtr(60), Windows: []GenericWindow{{Name: "5h", RemainingPercent: floatPtr(60)}}}}
	source := &combinedSource{sources: []namedSource{{name: "codex", source: codex}, {name: "claude", source: &pacedSource{Source: claude}}}}
	defer source.Close()
	for i := 0; i < 2; i++ {
		snapshot, err := source.Read()
		if err != nil || len(snapshot.Accounts) != 2 || len(snapshot.Windows) != 2 || *snapshot.FiveHourRemainingPercent != 60 {
			t.Fatalf("combined snapshot = %#v, %v", snapshot, err)
		}
		values := snapshotValues(snapshot)
		if len(values) != 2 || values["Claude / 5h"] != 75 || values["Codex / 5h"] != 60 {
			t.Fatalf("provider windows = %#v", values)
		}
	}
	if claude.reads != 1 {
		t.Fatal("Claude endpoint was polled more often than once a minute")
	}
	codex.err = errors.New("Codex temporarily unavailable")
	snapshot, err := source.Read()
	if err != nil || len(snapshot.Windows) != 1 || snapshot.Accounts[0].Error == "" || snapshot.Accounts[1].Snapshot == nil {
		t.Fatalf("provider failure hid Claude: %#v, %v", snapshot, err)
	}
	var output strings.Builder
	PrintSnapshot(&output, snapshot)
	if !strings.Contains(output.String(), "Codex:") || !strings.Contains(output.String(), "Unavailable:") || !strings.Contains(output.String(), "Claude:") || !strings.Contains(output.String(), "75%") {
		t.Fatalf("partial snapshot text = %s", &output)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || !strings.Contains(string(encoded), `"accounts"`) || !strings.Contains(string(encoded), `"provider":"claude"`) {
		t.Fatalf("combined JSON = %s, %v", encoded, err)
	}
	source.sources[1].source = &unavailableSource{err: errors.New("Claude unavailable")}
	if _, err := source.Read(); err == nil {
		t.Fatal("a failure of every provider was treated as a successful snapshot")
	}
}

func TestAutoDetectsClaudeWithoutTokenInputAndDoesNotAffectExplicitCodex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	t.Setenv("CODEX_LIMITS_CODEX_BIN", filepath.Join(home, "missing-codex"))
	path := filepath.Join(home, "claude", ".credentials.json")
	writeClaudeAuth(t, path, `{"claudeAiOauth":{"accessToken":"existing-login"}}`)
	source, err := OpenSource("auto", "", false)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if claude, ok := source.(*ClaudeSource); !ok || claude.Path != path {
		t.Fatalf("automatic source = %T", source)
	}
	if _, err := OpenSource("codex", "", false); err == nil {
		t.Fatal("explicit Codex selection switched to Claude")
	}
	writeClaudeAuth(t, path, `{"ANTHROPIC_API_KEY":"api-key"}`)
	if _, err := OpenSource("auto", "", false); err == nil {
		t.Fatal("an API key was accepted as subscription login")
	}
}

func TestAutoDetectsBothAccountsFromNativeFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	t.Setenv("CODEX_LIMITS_CODEX_BIN", filepath.Join(home, "missing-codex"))
	writeClaudeAuth(t, filepath.Join(home, "codex", "auth.json"), `{"tokens":{"access_token":"codex-token","account_id":"account"}}`)
	writeClaudeAuth(t, filepath.Join(home, "claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"claude-token"}}`)
	source, err := OpenSource("auto", "", false)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if combined, ok := source.(*combinedSource); !ok || len(combined.sources) != 2 || combined.sources[0].name != "codex" || combined.sources[1].name != "claude" {
		t.Fatalf("automatic source = %T", source)
	}
}

func TestClaudeAndCombinedHistoryRemainSeparateAndRestoreAllSeries(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "oauth.json")
	claude := NewClaudeSource(path)
	defer claude.Close()
	codex := NewFileSource(path)
	claudeStore, err := newHistoryStore(claude)
	if err != nil {
		t.Fatal(err)
	}
	codexStore, err := newHistoryStore(codex)
	if err != nil {
		t.Fatal(err)
	}
	combined := &combinedSource{sources: []namedSource{{name: "codex", source: codex}, {name: "claude", source: &pacedSource{Source: claude}}}}
	combinedStore, err := newHistoryStore(combined)
	if err != nil {
		t.Fatal(err)
	}
	if claudeStore.directory == codexStore.directory || combinedStore.directory == claudeStore.directory || combinedStore.directory == codexStore.directory {
		t.Fatal("different provider selections share history")
	}
	now := time.Now()
	snapshot := &Snapshot{Provider: "all", Accounts: []AccountSnapshot{{Provider: "claude"}}, Windows: []GenericWindow{
		{Name: "Claude / 5h", RemainingPercent: floatPtr(75)}, {Name: "Codex / 5h", RemainingPercent: floatPtr(60)},
	}}
	if err := combinedStore.append(historyRecord{At: now, Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	reopened, err := newHistoryStore(combined)
	if err != nil {
		t.Fatal(err)
	}
	history, err := reopened.load(now.Add(time.Second))
	if err != nil || len(history) != 1 || len(history[0].Values) != 2 || history[0].Values["Claude / 5h"] != 75 {
		t.Fatalf("restored history = %#v, %v", history, err)
	}
	wrapped := withHistory(claude, io.Discard)
	if provider, ok := wrapped.(interface{ Provider() string }); !ok || provider.Provider() != "claude" {
		t.Fatal("history wrapper lost provider information for initial chart colors")
	}
}

func TestClaudeChartAndLegendAreOrangeInSelectedAndCombinedModes(t *testing.T) {
	t.Setenv("COLUMNS", "120")
	t.Setenv("LINES", "32")
	now := time.Now()
	for _, combined := range []bool{false, true} {
		provider, name := "claude", "5h"
		if combined {
			provider, name = "all", "Claude / 5h"
		}
		snapshot := Snapshot{Provider: provider, Windows: []GenericWindow{{Name: name, RemainingPercent: floatPtr(75)}}}
		history := []Sample{{At: now, Values: map[string]float64{name: 75}}}
		var output strings.Builder
		dashboard(&output, io.Discard, history, &snapshot, 60, now, "", true, now)
		if strings.Count(output.String(), "\x1b[38;5;166m") < 2 {
			t.Fatalf("Claude chart or legend is not orange: %q", output.String())
		}
		output.Reset()
		dashboard(&output, io.Discard, history, &snapshot, 60, now, "", false, now)
		if strings.Contains(output.String(), "\x1b[") {
			t.Fatal("color-disabled Claude chart contains ANSI escapes")
		}
	}
	if colors := seriesColors([]string{"Claude / 5h", "Claude / Weekly", "Codex / 5h", "Codex / Weekly"}, "all"); colors[0] != 166 || colors[1] != 214 || colors[2] != palette[0] || colors[3] != palette[1] {
		t.Fatal("Claude changed the existing Codex series colors")
	}
}

func TestClaudeOptionsKeepManualOverridesAndRejectConflicts(t *testing.T) {
	for _, args := range [][]string{{"--source", "claude"}, {"--source", "claude", "--auth-file", "oauth.json"}} {
		if opts, err := parseOptions(args); err != nil || opts.interval != 60 {
			t.Fatalf("Claude options = %#v, %v", opts, err)
		}
	}
	if opts, err := parseOptions([]string{"--source", "claude", "--live", "--interval", "90"}); err != nil || opts.interval != 90 {
		t.Fatal("explicit interval was not honored")
	}
	if _, err := parseOptions([]string{"--source", "claude", "--demo"}); err == nil {
		t.Fatal("Claude demo/source conflict was accepted")
	}
	if _, err := OpenSource("claude", "", true); err == nil {
		t.Fatal("empty explicit auth path was accepted")
	}
	if opts, err := parseOptions(nil); err != nil || opts.interval != 5 {
		t.Fatal("Codex's default refresh interval changed")
	}
}
