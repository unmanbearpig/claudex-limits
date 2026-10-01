package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"

type claudeCredentials struct {
	AccessToken string
	PlanType    string
}

func parseClaudeCredentials(contents []byte, now time.Time) (claudeCredentials, error) {
	var raw map[string]any
	if json.Unmarshal(contents, &raw) != nil || raw == nil {
		return claudeCredentials{}, errors.New("invalid Claude OAuth credentials")
	}
	if disabled, _ := raw["disabled"].(bool); disabled {
		return claudeCredentials{}, errors.New("Claude OAuth file is disabled")
	}
	oauth := objectField(raw, "claudeAiOauth")
	if oauth == nil {
		// CLIProxyAPI stores Claude credentials as a flat OAuth object.
		if kind := stringField(raw, "type"); kind != "" && kind != "claude" {
			return claudeCredentials{}, errors.New("OAuth file is not a Claude account")
		}
		oauth = raw
	}
	access := strings.TrimSpace(stringField(oauth, "accessToken", "access_token"))
	if access == "" {
		return claudeCredentials{}, errors.New("Claude OAuth credentials have no access token; run `claude auth login`")
	}
	if scopes, present := oauth["scopes"]; present {
		hasProfile := false
		for _, scope := range mapScopes(scopes) {
			if scope == "user:profile" {
				hasProfile = true
			}
		}
		if !hasProfile {
			return claudeCredentials{}, errors.New("Claude OAuth token lacks user:profile scope for usage; run `claude auth login`")
		}
	}
	var expiresAt time.Time
	if millis, ok := numberField(oauth, "expiresAt"); ok && millis > 0 {
		expiresAt, _ = parseTimestamp(millis / 1000)
	} else {
		for _, name := range []string{"expires_at", "expired"} {
			if when, ok := parseTimestamp(oauth[name]); ok {
				expiresAt = when
				break
			}
		}
	}
	if !expiresAt.IsZero() && !expiresAt.After(now) {
		return claudeCredentials{}, errors.New("Claude OAuth token has expired; reopen Claude Code to refresh it, or run `claude auth login`")
	}
	return claudeCredentials{AccessToken: access, PlanType: stringField(oauth, "subscriptionType", "subscription_type")}, nil
}

func mapScopes(raw any) []string {
	if text, ok := raw.(string); ok {
		return strings.Fields(text)
	}
	var scopes []string
	if list, ok := raw.([]any); ok {
		for _, item := range list {
			if text, ok := item.(string); ok {
				scopes = append(scopes, text)
			}
		}
	}
	return scopes
}

func claudeConfigDirectory() string {
	if custom := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); custom != "" {
		return filepath.Clean(expandPath(custom))
	}
	return filepath.Join(homeDirectory(), ".claude")
}

func claudeKeychainService(configDirectory string) string {
	service := "Claude Code-credentials"
	if strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")) != "" {
		// Claude Code hashes the configuration string exactly as supplied, even
		// when it is relative or has a trailing slash.
		hash := sha256.Sum256([]byte(configDirectory))
		service += fmt.Sprintf("-%x", hash[:4])
	}
	return service
}

func readClaudeKeychain(ctx context.Context, service string) ([]byte, error) {
	// Use Claude Code's own reader. Keychain access belongs to the executable:
	// osascript cannot use a credential already authorized for /usr/bin/security.
	account := os.Getenv("USER")
	if account == "" {
		if current, err := user.Current(); err == nil {
			account = current.Username
		}
	}
	args := []string{"find-generic-password", "-s", service, "-w"}
	if account != "" {
		args = append(args, "-a", account)
	}
	return claudeKeychainCommandOutput(exec.CommandContext(ctx, "/usr/bin/security", args...))
}

func claudeKeychainCommandOutput(command *exec.Cmd) ([]byte, error) {
	contents, err := command.Output()
	if err != nil {
		// security exits with the low byte of its OSStatus. Never include its
		// stdout, stderr, or exec error, which may contain credential contents.
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			switch exit.ExitCode() {
			case 44: // errSecItemNotFound (-25300)
				return nil, os.ErrNotExist
			case 36, 51, 128: // interaction not allowed, authentication failed, canceled
				return nil, errors.New("macOS Keychain denied access to Claude OAuth credentials; allow /usr/bin/security to access the Claude Code Keychain item, or use --auth-file")
			}
			return nil, fmt.Errorf("could not read Claude OAuth credentials from macOS Keychain (security exit %d)", exit.ExitCode())
		}
		return nil, errors.New("could not run the macOS Keychain credential reader")
	}
	if len(strings.TrimSpace(string(contents))) == 0 {
		return nil, errors.New("Claude OAuth credentials in macOS Keychain are empty; run `claude auth login`")
	}
	return contents, nil
}

// ClaudeSource reads Claude Code's login without refreshing or editing it.
// Path also identifies its history when credentials live in the Keychain.
type ClaudeSource struct {
	Path            string
	Client          HTTPDoer
	keychainService string
	keychainReader  func(context.Context, string) ([]byte, error)
	ctx             context.Context
	cancel          context.CancelFunc
	mu              sync.Mutex
	retryAt         time.Time
	agentOnce       sync.Once
	userAgent       string
}

func NewClaudeSource(path string) *ClaudeSource {
	ctx, cancel := context.WithCancel(context.Background())
	return &ClaudeSource{
		Path: path, Client: &http.Client{Timeout: requestTimeout}, ctx: ctx, cancel: cancel,
	}
}

func (s *ClaudeSource) Close() error {
	s.cancel()
	return nil
}

func (s *ClaudeSource) usageUserAgent(ctx context.Context) string {
	s.agentOnce.Do(func() {
		if s.userAgent != "" {
			return
		}
		// The account endpoint uses Claude Code's OAuth client protocol. Match
		// its installed version without starting an authenticated CLI session.
		cliVersion := "2.1.0"
		versionCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if output, err := exec.CommandContext(versionCtx, "claude", "--version").Output(); err == nil {
			fields := strings.Fields(string(output))
			if len(fields) > 0 && regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+[a-zA-Z0-9.+-]*$`).MatchString(fields[0]) {
				cliVersion = fields[0]
			}
		}
		s.userAgent = "claude-code/" + cliVersion
	})
	return s.userAgent
}

func (s *ClaudeSource) credentials(ctx context.Context) (claudeCredentials, error) {
	if s.keychainService != "" {
		contents, err := s.keychainReader(ctx, s.keychainService)
		if err == nil {
			return parseClaudeCredentials(contents, time.Now())
		}
		if !errors.Is(err, os.ErrNotExist) {
			return claudeCredentials{}, err
		}
	}
	contents, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return claudeCredentials{}, fmt.Errorf("Claude OAuth file not found: %s; run `claude auth login`", s.Path)
	}
	if err != nil {
		return claudeCredentials{}, fmt.Errorf("could not read Claude OAuth file: %s", s.Path)
	}
	return parseClaudeCredentials(contents, time.Now())
}

func findClaudeProxyAuth() (string, error) {
	entries, err := os.ReadDir(proxyAuthDirectory())
	if err != nil {
		return "", errors.New("no Claude proxy OAuth file is available")
	}
	var path string
	var newest time.Time
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "claude-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		candidate := filepath.Join(proxyAuthDirectory(), entry.Name())
		contents, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		if _, err := parseClaudeCredentials(contents, time.Now()); err != nil {
			continue
		}
		info, err := entry.Info()
		if err == nil && (path == "" || info.ModTime().After(newest)) {
			path, newest = candidate, info.ModTime()
		}
	}
	if path == "" {
		return "", errors.New("no active Claude proxy OAuth file is available")
	}
	return path, nil
}

func OpenClaudeSource(authFile string) (*ClaudeSource, error) {
	if authFile != "" {
		return NewClaudeSource(filepath.Clean(expandPath(authFile))), nil
	}
	directory := claudeConfigDirectory()
	source := NewClaudeSource(filepath.Join(directory, ".credentials.json"))
	if runtime.GOOS == "darwin" {
		source.keychainService = claudeKeychainService(os.Getenv("CLAUDE_CONFIG_DIR"))
		source.keychainReader = readClaudeKeychain
		return source, nil
	}
	if _, err := os.Stat(source.Path); !errors.Is(err, os.ErrNotExist) {
		return source, nil
	}
	// An explicitly selected configuration must not fall back to another account.
	if strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")) == "" {
		if path, err := findClaudeProxyAuth(); err == nil {
			source.Path = path
		}
	}
	return source, nil
}

func claudeRetryAt(value string, now time.Time) time.Time {
	minimum := now.Add(time.Minute)
	if seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil && isFinite(seconds) && seconds >= 0 && seconds < float64(1<<63)/float64(time.Second) {
		if until := now.Add(time.Duration(seconds * float64(time.Second))); until.After(minimum) {
			return until
		}
	} else if until, err := http.ParseTime(value); err == nil && until.After(minimum) {
		return until
	}
	return minimum
}

func (s *ClaudeSource) Read() (Snapshot, error) {
	s.mu.Lock()
	retryAt := s.retryAt
	s.mu.Unlock()
	if time.Now().Before(retryAt) {
		return Snapshot{}, fmt.Errorf("Claude usage is rate limited; retry after %s", retryAt.Local().Format(time.RFC3339))
	}
	ctx, cancel := context.WithTimeout(s.ctx, requestTimeout)
	defer cancel()
	auth, err := s.credentials(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeUsageURL, nil)
	if err != nil {
		return Snapshot{}, errors.New("could not prepare Claude usage request")
	}
	request.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	request.Header.Set("anthropic-beta", "oauth-2025-04-20")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", s.usageUserAgent(ctx))
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return Snapshot{}, errors.New("Claude usage request timed out")
		}
		return Snapshot{}, errors.New("could not reach Claude for usage")
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusUnauthorized:
		return Snapshot{}, errors.New("Claude rejected the saved OAuth token; reopen Claude Code to refresh it, or run `claude auth login`")
	case http.StatusForbidden:
		return Snapshot{}, errors.New("Claude denied usage access; log in with a Claude subscription using `claude auth login`")
	case http.StatusTooManyRequests:
		s.mu.Lock()
		s.retryAt = claudeRetryAt(response.Header.Get("Retry-After"), time.Now())
		retryAt = s.retryAt
		s.mu.Unlock()
		return Snapshot{}, fmt.Errorf("Claude usage is rate limited; retry after %s", retryAt.Local().Format(time.RFC3339))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Snapshot{}, fmt.Errorf("Claude usage request failed with HTTP %d", response.StatusCode)
	}
	var raw map[string]any
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&raw); err != nil || raw == nil {
		return Snapshot{}, errors.New("Claude returned an invalid usage response")
	}
	snapshot := NormalizeClaudeUsage(raw, time.Now())
	snapshot.PlanType = auth.PlanType
	return snapshot, nil
}

func claudeBucketName(key string) string {
	if key == "oauth_apps" {
		return "OAuth apps"
	}
	words := strings.Fields(strings.ReplaceAll(key, "_", " "))
	for i, word := range words {
		words[i] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}

func NormalizeClaudeUsage(data map[string]any, now time.Time) Snapshot {
	result := Snapshot{Provider: "claude", Windows: []GenericWindow{}, AdditionalRateLimits: []AdditionalLimit{}}
	indices := make(map[string]int)
	add := func(raw map[string]any, name string, seconds int64, percentField string) {
		var remaining *float64
		if used, ok := numberField(raw, percentField); ok {
			remaining = floatPtr(clampFloat(100-used, 0, 100))
		}
		reset := makeReset(raw["resets_at"], now)
		if remaining == nil && reset == nil {
			return
		}
		if index, found := indices[name]; found {
			if result.Windows[index].RemainingPercent == nil {
				result.Windows[index].RemainingPercent = remaining
			}
			if result.Windows[index].Reset == nil {
				result.Windows[index].Reset = reset
			}
			return
		}
		indices[name] = len(result.Windows)
		result.Windows = append(result.Windows, GenericWindow{Name: name, RemainingPercent: remaining, Reset: reset, WindowSeconds: seconds})
	}
	add(objectField(data, "five_hour"), "5h", FiveHourSeconds, "utilization")
	add(objectField(data, "seven_day"), "Weekly", WeekSeconds, "utilization")
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key == "five_hour" || key == "seven_day" || key == "extra_usage" {
			continue
		}
		raw := objectField(data, key)
		if raw == nil {
			continue
		}
		name, seconds := claudeBucketName(key), int64(0)
		if strings.HasPrefix(key, "seven_day_") {
			name = claudeBucketName(strings.TrimPrefix(key, "seven_day_")) + " / Weekly"
			seconds = WeekSeconds
		} else if strings.HasPrefix(key, "five_hour_") {
			name = claudeBucketName(strings.TrimPrefix(key, "five_hour_")) + " / 5h"
			seconds = FiveHourSeconds
		}
		add(raw, name, seconds, "utilization")
	}
	for _, value := range arrayField(data, "limits") {
		raw := mapRaw(value)
		kind, group := stringField(raw, "kind"), stringField(raw, "group")
		name, seconds := claudeBucketName(kind), int64(0)
		scope := objectField(raw, "scope")
		model := objectField(scope, "model")
		bucket := stringField(model, "display_name", "id")
		if bucket == "" {
			bucket = stringField(objectField(scope, "surface"), "display_name", "id")
		}
		if bucket == "" {
			bucket = stringField(scope, "surface")
		}
		switch {
		case kind == "session" || group == "session":
			name, seconds = "5h", FiveHourSeconds
		case strings.HasPrefix(kind, "weekly_") || group == "weekly":
			name, seconds = "Weekly", WeekSeconds
		}
		if strings.Contains(kind, "scoped") && bucket == "" {
			continue // An unnamed scoped limit cannot stand in for the account total.
		}
		if bucket != "" {
			name = bucket + " / " + name
		}
		if name != "" {
			add(raw, name, seconds, "percent")
		}
	}
	for _, window := range result.Windows {
		switch window.Name {
		case "5h":
			result.FiveHourRemainingPercent, result.FiveHourReset = window.RemainingPercent, window.Reset
		case "Weekly":
			result.WeeklyRemainingPercent, result.WeeklyReset = window.RemainingPercent, window.Reset
		default:
			if strings.HasSuffix(window.Name, " / Weekly") {
				result.AdditionalRateLimits = append(result.AdditionalRateLimits, AdditionalLimit{Name: strings.TrimSuffix(window.Name, " / Weekly"), WeeklyRemainingPercent: window.RemainingPercent, WeeklyReset: window.Reset})
			} else if strings.HasSuffix(window.Name, " / 5h") {
				result.AdditionalRateLimits = append(result.AdditionalRateLimits, AdditionalLimit{Name: strings.TrimSuffix(window.Name, " / 5h"), FiveHourRemainingPercent: window.RemainingPercent, FiveHourReset: window.Reset})
			}
		}
	}
	if raw := objectField(data, "extra_usage"); raw != nil {
		enabled, _ := raw["is_enabled"].(bool)
		extra := &ExtraUsage{IsEnabled: enabled, Currency: stringField(raw, "currency")}
		if limit, ok := numberField(raw, "monthly_limit"); ok && limit >= 0 {
			extra.MonthlyLimit = floatPtr(limit)
		}
		if used, ok := numberField(raw, "used_credits"); ok && used >= 0 {
			extra.UsedCredits = floatPtr(used)
		}
		if enabled && extra.MonthlyLimit != nil && *extra.MonthlyLimit > 0 {
			if used, ok := numberField(raw, "utilization"); ok {
				extra.RemainingPercent = floatPtr(clampFloat(100-used, 0, 100))
			} else if extra.MonthlyLimit != nil && *extra.MonthlyLimit > 0 && extra.UsedCredits != nil {
				extra.RemainingPercent = floatPtr(clampFloat(100-100*(*extra.UsedCredits)/(*extra.MonthlyLimit), 0, 100))
			}
			if extra.RemainingPercent != nil {
				result.Windows = append(result.Windows, GenericWindow{Name: "Extra usage / Monthly", RemainingPercent: extra.RemainingPercent})
			}
		}
		result.ExtraUsage = extra
	}
	return result
}
