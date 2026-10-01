package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func writeClaudeAuth(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func claudeTestSource(t *testing.T, body string) *ClaudeSource {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".credentials.json")
	writeClaudeAuth(t, path, `{"claudeAiOauth":{"accessToken":"secret-token","scopes":["user:profile","user:inference"],"subscriptionType":"max"}}`)
	source := NewClaudeSource(path)
	source.userAgent = "claude-code/2.1.285"
	t.Cleanup(func() { _ = source.Close() })
	source.Client = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != claudeUsageURL || request.Method != http.MethodGet || request.Body != nil {
			t.Fatal("usage must be a read-only GET to the Claude OAuth endpoint")
		}
		if request.Header.Get("Authorization") != "Bearer secret-token" || request.Header.Get("anthropic-beta") != "oauth-2025-04-20" || request.Header.Get("User-Agent") != "claude-code/2.1.285" {
			t.Fatal("missing Claude OAuth headers")
		}
		if request.Header.Get("ChatGPT-Account-Id") != "" || request.Header.Get("x-api-key") != "" {
			t.Fatal("unrelated provider credentials sent to Claude")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	return source
}

func TestClaudeReadUsesExistingLoginAndSeesCredentialRotation(t *testing.T) {
	source := claudeTestSource(t, `{"five_hour":{"utilization":27.5},"seven_day":{"utilization":51}}`)
	snapshot, err := source.Read()
	if err != nil || snapshot.Provider != "claude" || snapshot.PlanType != "max" || *snapshot.FiveHourRemainingPercent != 72.5 || *snapshot.WeeklyRemainingPercent != 49 {
		t.Fatalf("Claude snapshot = %#v, %v", snapshot, err)
	}
	contents, err := os.ReadFile(source.Path)
	if err != nil || !strings.Contains(string(contents), "secret-token") {
		t.Fatal("monitor changed the credential file")
	}
	writeClaudeAuth(t, source.Path, `{"claudeAiOauth":{"accessToken":"rotated-token"}}`)
	source.Client = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer rotated-token" {
			t.Fatal("credential changes were not picked up")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	if _, err := source.Read(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || strings.Contains(string(encoded), "secret-token") || strings.Contains(string(encoded), "accessToken") {
		t.Fatal("credentials leaked to the JSON snapshot")
	}
}

func TestClaudeCredentialsRejectAPIKeysExpiredTokensAndMissingProfileScope(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	cases := []string{
		`{"ANTHROPIC_API_KEY":"secret-token"}`,
		`{"claudeAiOauth":{"accessToken":"secret-token","expiresAt":1790600000000}}`,
		`{"claudeAiOauth":{"accessToken":"secret-token","scopes":["user:inference"]}}`,
		`{"access_token":"secret-token","type":"codex"}`,
		`{"access_token":"secret-token","disabled":true}`,
		`{"secret-token":`,
		`null`,
	}
	for _, data := range cases {
		if _, err := parseClaudeCredentials([]byte(data), now); err == nil || strings.Contains(err.Error(), "secret-token") {
			t.Fatalf("credential error was missing or leaked a token: %v", err)
		}
	}
	for _, data := range []string{
		`{"claudeAiOauth":{"accessToken":"secret-token","expiresAt":1791000000000,"scopes":["user:profile"]}}`,
		`{"access_token":"secret-token","type":"claude","scopes":"user:profile user:inference","expired":"2026-10-01T00:00:00Z"}`,
	} {
		if _, err := parseClaudeCredentials([]byte(data), now); err != nil {
			t.Fatalf("valid credential rejected: %v", err)
		}
	}
}

func TestClaudeDiscoveryHonorsConfigExplicitFileAndNewestActiveProxy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	directory := filepath.Join(home, ".config", "cliproxyapi", "auth")
	first, newest := filepath.Join(directory, "claude-first.json"), filepath.Join(directory, "claude-newest.json")
	for _, path := range []string{first, newest} {
		writeClaudeAuth(t, path, `{"type":"claude","access_token":"proxy-token"}`)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(first, old, old); err != nil {
		t.Fatal(err)
	}
	writeClaudeAuth(t, filepath.Join(directory, "claude-disabled.json"), `{"type":"claude","access_token":"wrong-token","disabled":true}`)
	if path, err := findClaudeProxyAuth(); err != nil || path != newest {
		t.Fatalf("proxy discovery = %q, %v", path, err)
	}
	config := filepath.Join(home, "custom-claude")
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	path := filepath.Join(config, ".credentials.json")
	writeClaudeAuth(t, path, `{"claudeAiOauth":{"accessToken":"native-token"}}`)
	source, err := OpenSource("claude", "", false)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if claude := source.(*ClaudeSource); claude.Path != path || !hasClaudeLogin(claude) {
		t.Fatal("custom Claude configuration was not discovered")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if source.(*ClaudeSource).Path != path {
		t.Fatal("a custom profile switched to a proxy account")
	}
	explicit, err := OpenSource("claude", newest, true)
	if err != nil {
		t.Fatal(err)
	}
	defer explicit.Close()
	if explicit.(*ClaudeSource).Path != newest || explicit.(*ClaudeSource).keychainService != "" {
		t.Fatal("explicit file did not select exactly that file")
	}
}

func TestClaudeKeychainFallbackAndProfileIsolation(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if service := claudeKeychainService("/selected/profile"); service != "Claude Code-credentials" {
		t.Fatal("default Keychain service changed")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "/selected/profile")
	service := claudeKeychainService("/selected/profile")
	if service == "Claude Code-credentials" || service == claudeKeychainService("/other/profile") {
		t.Fatal("custom Keychain profiles are not isolated")
	}
	source := claudeTestSource(t, `{}`)
	source.keychainService = service
	source.keychainReader = func(_ context.Context, name string) ([]byte, error) {
		if name != service {
			t.Fatal("incorrect Keychain service queried")
		}
		return []byte(`{"claudeAiOauth":{"accessToken":"keychain-token"}}`), nil
	}
	if auth, err := source.credentials(context.Background()); err != nil || auth.AccessToken != "keychain-token" {
		t.Fatal("Keychain login was not preferred")
	}
	source.keychainReader = func(context.Context, string) ([]byte, error) { return nil, os.ErrNotExist }
	if auth, err := source.credentials(context.Background()); err != nil || auth.AccessToken != "secret-token" {
		t.Fatal("missing Keychain entry did not fall back to the native file")
	}
	source.keychainReader = func(context.Context, string) ([]byte, error) { return nil, errors.New("access denied") }
	if _, err := source.credentials(context.Background()); err == nil {
		t.Fatal("Keychain denial switched accounts")
	}
}

func TestClaudeKeychainCommandOutput(t *testing.T) {
	for _, test := range []struct {
		name     string
		exitCode int
		output   string
		wantErr  string
		missing  bool
	}{
		{name: "credential", output: `{"claudeAiOauth":{"accessToken":"secret-token"}}`},
		{name: "missing", exitCode: 44, missing: true},
		{name: "interaction required", exitCode: 36, wantErr: "Keychain denied access"},
		{name: "access denied", exitCode: 51, output: "secret-token", wantErr: "Keychain denied access"},
		{name: "canceled", exitCode: 128, wantErr: "Keychain denied access"},
		{name: "invalid parameter", exitCode: 206, wantErr: "security exit 206"},
		{name: "empty", output: "\n", wantErr: "credentials in macOS Keychain are empty"},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=^TestClaudeKeychainCommandHelper$")
			command.Env = append(os.Environ(), "CLAUDEX_TEST_KEYCHAIN_HELPER=1", "CLAUDEX_TEST_KEYCHAIN_EXIT="+strconv.Itoa(test.exitCode), "CLAUDEX_TEST_KEYCHAIN_OUTPUT="+test.output)
			contents, err := claudeKeychainCommandOutput(command)
			if test.missing {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing credential error = %v", err)
				}
			} else if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("Keychain error = %v, want %q", err, test.wantErr)
				}
			} else if err != nil || string(contents) != test.output {
				t.Fatal("Keychain credential was not returned intact")
			}
			if err != nil && (len(contents) != 0 || strings.Contains(err.Error(), "secret-token")) {
				t.Fatal("Keychain failure disclosed credential contents")
			}
		})
	}
	if _, err := claudeKeychainCommandOutput(exec.Command(filepath.Join(t.TempDir(), "missing-reader"))); err == nil || strings.Contains(err.Error(), "missing-reader") {
		t.Fatal("failed to start reader safely")
	}
}

func TestClaudeKeychainCommandHelper(t *testing.T) {
	if os.Getenv("CLAUDEX_TEST_KEYCHAIN_HELPER") != "1" {
		return
	}
	_, _ = io.WriteString(os.Stdout, os.Getenv("CLAUDEX_TEST_KEYCHAIN_OUTPUT"))
	_, _ = io.WriteString(os.Stderr, "secret-token")
	code, _ := strconv.Atoi(os.Getenv("CLAUDEX_TEST_KEYCHAIN_EXIT"))
	os.Exit(code)
}

func TestClaudeNormalizationHandlesBothSchemasWithoutDuplicates(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	var raw map[string]any
	data := `{
 "five_hour":{"utilization":12.5,"resets_at":"2026-09-29T16:00:00.123456+00:00"},
 "seven_day":{"utilization":30,"resets_at":null},
 "seven_day_sonnet":{"utilization":10},"seven_day_opus":null,
 "limits":[
   {"kind":"session","group":"session","percent":12.5},
   {"kind":"weekly_all","group":"weekly","percent":30},
   {"kind":"weekly_scoped","group":"weekly","percent":10,"scope":{"model":{"display_name":"Sonnet"}},"is_active":false},
   {"kind":"weekly_scoped","group":"weekly","percent":42,"scope":{"model":{"display_name":"Fable"}},"is_active":false},
   {"kind":"weekly_scoped","group":"weekly","percent":5,"scope":null}
 ],
 "future_bucket":{"utilization":61},
 "extra_usage":{"is_enabled":true,"monthly_limit":2000,"used_credits":520}
}`
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		t.Fatal(err)
	}
	snapshot := NormalizeClaudeUsage(raw, now)
	if *snapshot.FiveHourRemainingPercent != 87.5 || *snapshot.WeeklyRemainingPercent != 70 || snapshot.FiveHourReset.AfterSeconds != 14400 || snapshot.BankedResets != nil {
		t.Fatalf("primary quotas = %#v", snapshot)
	}
	if len(snapshot.Windows) != 6 || len(snapshot.AdditionalRateLimits) != 2 {
		t.Fatalf("missing or duplicate quotas: %#v", snapshot.Windows)
	}
	values := snapshotValues(snapshot)
	if values["Sonnet / Weekly"] != 90 || values["Fable / Weekly"] != 58 || values["Extra usage / Monthly"] != 74 || values["Future Bucket"] != 39 {
		t.Fatalf("values = %#v", values)
	}
	for _, window := range snapshot.Windows {
		if window.Name == "Future Bucket" && window.WindowSeconds != 0 {
			t.Fatal("unknown duration was invented")
		}
	}
	var output strings.Builder
	PrintSnapshot(&output, snapshot)
	if strings.Count(output.String(), "90%") != 1 || !strings.Contains(output.String(), "74%") {
		t.Fatalf("text quotas = %s", &output)
	}
}

func TestClaudeNormalizationMissingValuesAreNotUnlimited(t *testing.T) {
	for _, data := range []string{
		`{}`,
		`{"five_hour":null,"seven_day":null}`,
		`{"five_hour":{"utilization":null,"resets_at":null},"seven_day":{"utilization":"NaN"}}`,
		`{"extra_usage":{"is_enabled":false,"utilization":0,"monthly_limit":1000}}`,
		`{"extra_usage":{"is_enabled":true,"monthly_limit":0,"used_credits":0,"utilization":0}}`,
		`{"extra_usage":{"is_enabled":true,"monthly_limit":null,"used_credits":0,"utilization":0}}`,
	} {
		var raw map[string]any
		if err := json.Unmarshal([]byte(data), &raw); err != nil {
			t.Fatal(err)
		}
		snapshot := NormalizeClaudeUsage(raw, time.Now())
		if len(snapshot.Windows) != 0 || snapshot.FiveHourRemainingPercent != nil || snapshot.WeeklyRemainingPercent != nil {
			t.Fatalf("invented remaining quota for %s", data)
		}
	}
	var raw map[string]any
	_ = json.Unmarshal([]byte(`{"five_hour":{"utilization":110},"seven_day":{"utilization":-5}}`), &raw)
	snapshot := NormalizeClaudeUsage(raw, time.Now())
	if *snapshot.FiveHourRemainingPercent != 0 || *snapshot.WeeklyRemainingPercent != 100 {
		t.Fatal("percentages were not clamped")
	}
}

func TestClaudeHTTPFailuresDoNotLeakSecretsAndBackOff(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500, 200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			source := claudeTestSource(t, `{}`)
			calls := 0
			source.Client = roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {"180"}}, Body: io.NopCloser(strings.NewReader("secret-token invalid body"))}, nil
			})
			if _, err := source.Read(); err == nil || strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("HTTP error missing or leaked credentials: %v", err)
			}
			if status == 429 {
				if _, err := source.Read(); err == nil || calls != 1 || time.Until(source.retryAt) < 170*time.Second {
					t.Fatal("rate-limited endpoint was retried before Retry-After")
				}
			}
		})
	}
	now := time.Now().UTC().Truncate(time.Second)
	for _, value := range []string{"", "garbage", "0", "-1", "NaN", "1e100", now.Add(-time.Hour).Format(http.TimeFormat)} {
		if until := claudeRetryAt(value, now); !until.Equal(now.Add(time.Minute)) {
			t.Fatalf("invalid Retry-After %q = %v", value, until)
		}
	}
	if until := claudeRetryAt(now.Add(5*time.Minute).Format(http.TimeFormat), now); !until.Equal(now.Add(5 * time.Minute)) {
		t.Fatal("HTTP date Retry-After was not honored")
	}
}

func TestClaudeCloseCancelsAnInFlightRequest(t *testing.T) {
	source := claudeTestSource(t, `{}`)
	started := make(chan struct{})
	source.Client = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	done := make(chan error, 1)
	go func() { _, err := source.Read(); done <- err }()
	<-started
	_ = source.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed request succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Claude request remained blocked after Close")
	}
}
