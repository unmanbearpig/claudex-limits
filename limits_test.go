package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestNormalizeUsageKeepsGenericWindowsAndDeduplicatesPrimary(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	raw := map[string]any{
		"planType": "future-plan",
		"rateLimits": map[string]any{
			"limitId":   "codex",
			"primary":   map[string]any{"usedPercent": 23.0, "windowDurationMins": 300.0},
			"secondary": map[string]any{"usedPercent": 50.0, "windowDurationMins": 10080.0},
		},
		"rateLimitsByLimitId": map[string]any{
			"codex": map[string]any{
				"limitId":   "codex",
				"primary":   map[string]any{"usedPercent": 23.0, "windowDurationMins": 300.0},
				"secondary": map[string]any{"usedPercent": 50.0, "windowDurationMins": 10080.0},
			},
			"other": map[string]any{"limitName": "Other", "primary": map[string]any{"usedPercent": 80.0, "windowDurationMins": 15.0}},
		},
	}
	snapshot := NormalizeUsage(raw, now)
	if snapshot.PlanType != "future-plan" {
		t.Fatalf("plan type = %q", snapshot.PlanType)
	}
	if snapshot.FiveHourRemainingPercent == nil || *snapshot.FiveHourRemainingPercent != 77 {
		t.Fatalf("five-hour remaining = %#v", snapshot.FiveHourRemainingPercent)
	}
	if len(snapshot.Windows) != 3 {
		t.Fatalf("windows = %#v", snapshot.Windows)
	}
	if snapshot.Windows[2].Name != "Other / 15m" || snapshot.Windows[2].WindowSeconds != 900 {
		t.Fatalf("generic window = %#v", snapshot.Windows[2])
	}
}

func TestNormalizeUsageMapOnlyChoosesCodexPrimary(t *testing.T) {
	raw := map[string]any{"rateLimitsByLimitId": map[string]any{
		"other": map[string]any{"limitName": "Other", "primary": map[string]any{"usedPercent": 90.0, "windowDurationMins": 15.0}},
		"codex": map[string]any{"primary": map[string]any{"usedPercent": 20.0, "windowDurationMins": 300.0}},
	}}
	snapshot := NormalizeUsage(raw, time.Now())
	if snapshot.FiveHourRemainingPercent == nil || *snapshot.FiveHourRemainingPercent != 80 {
		t.Fatalf("primary did not use codex bucket: %#v", snapshot)
	}
	if len(snapshot.Windows) != 2 || snapshot.Windows[1].Name != "Other / 15m" {
		t.Fatalf("generic additional = %#v", snapshot.Windows)
	}
}

func TestNormalizeUsageRetainsUnknownDurationWithoutInventingSeconds(t *testing.T) {
	snapshot := NormalizeUsage(map[string]any{"rateLimits": map[string]any{
		"primary": map[string]any{"usedPercent": 45.0, "windowDurationMins": nil},
	}}, time.Now())
	if len(snapshot.Windows) != 1 || snapshot.Windows[0].WindowSeconds != 0 {
		t.Fatalf("unknown duration window = %#v", snapshot.Windows)
	}
	if snapshot.Windows[0].RemainingPercent == nil || *snapshot.Windows[0].RemainingPercent != 55 {
		t.Fatalf("remaining = %#v", snapshot.Windows[0].RemainingPercent)
	}
}

func TestNormalizeUsageDirectAdditionalAndCredits(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	expires := now.Add(24 * time.Hour).Format(time.RFC3339)
	snapshot := NormalizeUsage(map[string]any{
		"rate_limit": map[string]any{"primary_window": map[string]any{"used_percent": 30.0, "limit_window_seconds": 18000.0}},
		"additional_rate_limits": []any{map[string]any{
			"limit_name": "Spark",
			"rate_limit": map[string]any{"primary_window": map[string]any{"used_percent": 10.0, "limit_window_seconds": 18000.0}},
		}},
		"rate_limit_reset_credits": map[string]any{"available_count": 0.0, "credits": nil},
	}, now)
	if snapshot.FiveHourRemainingPercent == nil || *snapshot.FiveHourRemainingPercent != 70 {
		t.Fatalf("legacy primary = %#v", snapshot)
	}
	if len(snapshot.AdditionalRateLimits) != 1 || snapshot.AdditionalRateLimits[0].Name != "Spark" {
		t.Fatalf("legacy additional = %#v", snapshot.AdditionalRateLimits)
	}
	if snapshot.BankedResets == nil || snapshot.BankedResets.Available == nil || *snapshot.BankedResets.Available != 0 || snapshot.BankedResets.ExpirationDetailsPartial {
		t.Fatalf("zero credits = %#v", snapshot.BankedResets)
	}

	detail := normalizeCredits(map[string]any{"available_count": 2.0, "credits": []any{
		map[string]any{"status": "available", "expires_at": expires},
		map[string]any{"status": "available", "expires_at": now.Add(48 * time.Hour).Format(time.RFC3339)},
	}}, now)
	merged := mergeCreditDetails(&BankedResets{Available: intPtr(2), ExpirationDetailsPartial: true}, detail, map[string]any{"available_count": 2.0, "credits": []any{}}, now)
	if merged.ExpirationDetailsPartial {
		t.Fatal("complete details remained partial")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) Do(request *http.Request) (*http.Response, error) { return f(request) }

func TestFileSourceSupportsNativeCredentialsAndOptionalDetails(t *testing.T) {
	directory := t.TempDir()
	authPath := filepath.Join(directory, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"tokens":{"access_token":"secret-token","account_id":"acct"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests []string
	source := NewFileSource(authPath)
	source.Client = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		requests = append(requests, request.URL.Path)
		mu.Unlock()
		if request.Header.Get("Authorization") != "Bearer secret-token" || request.Header.Get("ChatGPT-Account-Id") != "acct" {
			return nil, errors.New("headers not copied")
		}
		body := `{"rate_limit":{"primary_window":{"used_percent":25,"limit_window_seconds":18000}},"rate_limit_reset_credits":{"available_count":1}}`
		if strings.HasSuffix(request.URL.Path, "rate-limit-reset-credits") {
			body = `{"available_count":1,"credits":null}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	snapshot, err := source.Read()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.FiveHourRemainingPercent == nil || *snapshot.FiveHourRemainingPercent != 75 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.BankedResets == nil || snapshot.BankedResets.Available == nil || *snapshot.BankedResets.Available != 1 || !snapshot.BankedResets.ExpirationDetailsPartial {
		t.Fatalf("credits = %#v", snapshot.BankedResets)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %v", requests)
	}
}

func TestCredentialDiscoveryPrefersNativeAndNewestActiveProxy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proxyDirectory := filepath.Join(home, ".config", "cliproxyapi", "auth")
	if err := os.MkdirAll(proxyDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(proxyDirectory, "codex-first.json")
	second := filepath.Join(proxyDirectory, "codex-second.json")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte(`{"access_token":"token","account_id":"account"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(first, old, old); err != nil {
		t.Fatal(err)
	}
	path, err := findProxyAuth()
	if err != nil || path != second {
		t.Fatalf("newest proxy = %q, %v", path, err)
	}
	custom := filepath.Join(home, "custom-codex")
	if err := os.MkdirAll(custom, 0700); err != nil {
		t.Fatal(err)
	}
	native := filepath.Join(custom, "auth.json")
	if err := os.WriteFile(native, []byte(`{"tokens":{"access_token":"native","account_id":"native-account"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", custom)
	if found, ok := findNativeAuth(); !ok || found != native {
		t.Fatalf("native auth = %q, %v", found, ok)
	}
	if source, err := OpenSource("auto", native, true); err != nil {
		t.Fatal(err)
	} else if file, ok := source.(*FileSource); !ok || file.Path != native {
		t.Fatalf("explicit source = %#v", source)
	}
}

func TestCredentialErrorsDoNotExposeTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"OPENAI_API_KEY":"secret-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentials(path); err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("credential error leaked data: %v", err)
	}
}

func TestChartOrientationGapsAndRetention(t *testing.T) {
	end := time.Unix(1_800_000_000, 0)
	history := []Sample{{At: end.Add(-10 * time.Second), Values: map[string]float64{"5h": 100}}, {At: end.Add(-5 * time.Second), Values: nil}, {At: end, Values: map[string]float64{"5h": 0}}}
	lines := ChartLines(history, end, 50, 8, []string{"5h"}, false, 5)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "100%") || !strings.Contains(joined, "  0%") {
		t.Fatalf("axis labels missing: %s", joined)
	}
	if !hasBraille(lines[1]) || !hasBraille(lines[8]) {
		t.Fatalf("100%% and 0%% samples were plotted in the wrong rows: %q / %q", lines[1], lines[8])
	}
	for _, line := range lines[2:8] {
		if hasBraille(line) {
			t.Fatalf("missed refresh was connected across a gap: %q", line)
		}
	}
	historyWithOld := append([]Sample{{At: end.Add(-time.Duration(HistorySeconds)*time.Second - time.Second), Values: map[string]float64{"5h": 1}}}, history...)
	if len(trimHistory(historyWithOld, end)) != 3 {
		t.Fatal("history retention did not drop only old samples")
	}
}

func hasBraille(line string) bool {
	for _, character := range line {
		if character >= 0x2800 && character <= 0x28ff {
			return true
		}
	}
	return false
}

func TestParseOptionsUsesExplicitFlagVisits(t *testing.T) {
	if _, err := parseOptions([]string{"-interval", "2"}); err == nil || !strings.Contains(err.Error(), "requires --live") {
		t.Fatalf("single-dash interval was not rejected: %v", err)
	}
	if _, err := parseOptions([]string{"--json", "--live"}); err == nil {
		t.Fatal("json/live conflict was accepted")
	}
	if _, err := parseOptions([]string{"--live", "--interval", "1e300"}); err == nil {
		t.Fatal("duration overflow was accepted")
	}
	if _, err := parseOptions([]string{"--live", "--interval", "1e-300"}); err == nil {
		t.Fatal("timer underflow was accepted")
	}
}

func TestSanitizeError(t *testing.T) {
	if got := sanitizeError("one\ntwo\rthree"); got != "one two three" {
		t.Fatalf("sanitized error = %q", got)
	}
}

func TestJSONSnapshotShape(t *testing.T) {
	snapshot := NormalizeUsage(map[string]any{}, time.Now())
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"windows":[]`) {
		t.Fatalf("empty windows missing: %s", encoded)
	}
}

func TestPrintSnapshotDoesNotDuplicateNamedAdditionalWindows(t *testing.T) {
	value := 80.0
	snapshot := Snapshot{
		Windows:              []GenericWindow{{Name: "Spark / 5h", RemainingPercent: &value, WindowSeconds: FiveHourSeconds}},
		AdditionalRateLimits: []AdditionalLimit{{Name: "Spark", FiveHourRemainingPercent: &value}},
	}
	var output strings.Builder
	PrintSnapshot(&output, snapshot)
	if strings.Count(output.String(), "80%") != 1 {
		t.Fatalf("additional window was printed more than once: %q", output.String())
	}

	output.Reset()
	PrintSnapshot(&output, Snapshot{BankedResets: &BankedResets{Available: intPtr(0)}})
	if !strings.Contains(output.String(), "Banked resets: 0") {
		t.Fatalf("banked reset without quotas was hidden: %q", output.String())
	}
}

func TestCodexExecutableAcceptsRenamedAndLegacyOverrides(t *testing.T) {
	for _, test := range []struct{ current, legacy, want string }{
		{"", "", "codex"},
		{" /new/codex ", " /old/codex ", "/new/codex"},
		{" \t", " /old/codex ", "/old/codex"},
	} {
		t.Run(test.want, func(t *testing.T) {
			t.Setenv("CLAUDEX_LIMITS_CODEX_BIN", test.current)
			t.Setenv("CODEX_LIMITS_CODEX_BIN", test.legacy)
			if got := codexExecutable(); got != test.want {
				t.Fatalf("Codex executable = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAppServerProtocolReusesChildAndCleansUp(t *testing.T) {
	directory := t.TempDir()
	script := filepath.Join(directory, "fake-codex")
	calls := filepath.Join(directory, "calls")
	program := `#!/bin/sh
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$CLAUDEX_LIMITS_FAKE_CALLS"
  id=$(printf '%s\n' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*) printf '{"id":%s,"result":{}}\n' "$id";;
    *'"method":"initialized"'*) :;;
    *'"method":"account/read"'*) printf '{"id":%s,"result":{"account":{"type":"chatgpt","planType":"future-plan"}}}\n' "$id";;
    *'"method":"account/rateLimits/read"'*) printf '{"id":%s,"result":{"rateLimits":{"primary":{"usedPercent":25,"windowDurationMins":15}}}}\n' "$id";;
    *) exit 9;;
  esac
done`
	if err := os.WriteFile(script, []byte(program), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDEX_LIMITS_CODEX_BIN", script)
	t.Setenv("CLAUDEX_LIMITS_FAKE_CALLS", calls)
	source, err := NewAppServerSource()
	if err != nil {
		t.Fatal(err)
	}
	first, err := source.Read()
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.Read()
	if err != nil {
		t.Fatal(err)
	}
	if first.PlanType != "future-plan" || len(second.Windows) != 1 || second.Windows[0].WindowSeconds != 900 {
		t.Fatalf("app-server snapshots = %#v / %#v", first, second)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	expectedMethods := []string{`"method":"initialize"`, `"method":"initialized"`, `"method":"account/read"`, `"method":"account/rateLimits/read"`, `"method":"account/rateLimits/read"`}
	if len(lines) != len(expectedMethods) {
		t.Fatalf("app-server calls = %q", lines)
	}
	for index, expected := range expectedMethods {
		if !strings.Contains(lines[index], expected) {
			t.Fatalf("app-server call %d = %q", index, lines[index])
		}
	}
	if strings.Contains(string(contents), "thread") || strings.Contains(string(contents), "turn") {
		t.Fatalf("non-read-only method sent: %s", contents)
	}
}

type blockingSource struct {
	done sync.Once
	stop chan struct{}
}

func (s *blockingSource) Read() (Snapshot, error) { <-s.stop; return Snapshot{}, errors.New("closed") }
func (s *blockingSource) Close() error {
	s.done.Do(func() { close(s.stop) })
	return nil
}

func TestRunLiveInterruptsBlockedRead(t *testing.T) {
	source := &blockingSource{stop: make(chan struct{})}
	result := make(chan error, 1)
	go func() { result <- RunLive(io.Discard, source, 1) }()
	time.Sleep(50 * time.Millisecond)
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("live loop did not stop after Ctrl+C")
	}
}

func TestAppServerStartupInterruptReapsChild(t *testing.T) {
	directory := t.TempDir()
	script := filepath.Join(directory, "stalling-codex")
	pidPath := filepath.Join(directory, "pid")
	program := `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*) printf '%s\n' "$$" > "$CLAUDEX_LIMITS_STALL_PID"; while IFS= read -r ignored; do :; done;;
  esac
done`
	if err := os.WriteFile(script, []byte(program), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDEX_LIMITS_CODEX_BIN", script)
	t.Setenv("CLAUDEX_LIMITS_STALL_PID", pidPath)
	result := make(chan error, 1)
	go func() {
		_, err := NewAppServerSource()
		result <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(pidPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stalling app server did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, errInterrupted) {
			t.Fatalf("startup error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("startup did not stop after Ctrl+C")
	}
	contents, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Sscanf(string(contents), "%d", &pid); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("app-server child %d is still alive", pid)
	}
}
