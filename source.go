package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	usageURL        = "https://chatgpt.com/backend-api/wham/usage"
	resetCreditsURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
	requestTimeout  = 15 * time.Second
)

// Source provides one account snapshot per call.
type Source interface {
	Read() (Snapshot, error)
	Close() error
}

type credentials struct {
	AccessToken string
	AccountID   string
}

func homeDirectory() string {
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		return home
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

func readCredentials(path string) (credentials, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return credentials{}, fmt.Errorf("OAuth file not found: %s", path)
		}
		return credentials{}, fmt.Errorf("could not read OAuth file: %s", path)
	}
	var raw map[string]any
	if err := json.Unmarshal(contents, &raw); err != nil {
		return credentials{}, fmt.Errorf("invalid Codex OAuth file: %s", path)
	}
	if disabled, ok := raw["disabled"].(bool); ok && disabled {
		return credentials{}, fmt.Errorf("Codex OAuth file is disabled: %s", path)
	}
	access := stringField(raw, "access_token", "accessToken")
	account := stringField(raw, "account_id", "accountId")
	if tokens := objectField(raw, "tokens"); tokens != nil {
		if access == "" {
			access = stringField(tokens, "access_token", "accessToken")
		}
		if account == "" {
			account = stringField(tokens, "account_id", "accountId")
		}
	}
	if access == "" || account == "" {
		return credentials{}, fmt.Errorf("OAuth file has no access token and account ID: %s; log in with the Codex CLI", path)
	}
	return credentials{AccessToken: access, AccountID: account}, nil
}

func findNativeAuth() (string, bool) {
	paths := []string{}
	if custom := strings.TrimSpace(os.Getenv("CODEX_HOME")); custom != "" {
		paths = append(paths, filepath.Join(custom, "auth.json"))
	}
	if home := homeDirectory(); home != "" {
		fallback := filepath.Join(home, ".codex", "auth.json")
		if len(paths) == 0 || paths[0] != fallback {
			paths = append(paths, fallback)
		}
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path, true
		}
	}
	return "", false
}

func expandPath(path string) string {
	path = os.ExpandEnv(path)
	if path == "~" {
		return homeDirectory()
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(homeDirectory(), strings.TrimPrefix(path, "~/"))
	}
	return path
}

func proxyAuthDirectory() string {
	if home := homeDirectory(); home != "" {
		return filepath.Join(home, ".config", "cliproxyapi", "auth")
	}
	return filepath.Join(".config", "cliproxyapi", "auth")
}

func findProxyAuth() (string, error) {
	entries, err := os.ReadDir(proxyAuthDirectory())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("no active Codex OAuth file found in %s", proxyAuthDirectory())
		}
		return "", fmt.Errorf("could not inspect %s", proxyAuthDirectory())
	}
	type candidate struct {
		path string
		mod  time.Time
	}
	var candidates []candidate
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "codex-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(proxyAuthDirectory(), entry.Name())
		if _, err := readCredentials(path); err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{path: path, mod: info.ModTime()})
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no active Codex OAuth file found in %s", proxyAuthDirectory())
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].mod.Equal(candidates[j].mod) {
			return candidates[i].path < candidates[j].path
		}
		return candidates[i].mod.After(candidates[j].mod)
	})
	return candidates[0].path, nil
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type FileSource struct {
	Path   string
	Client HTTPDoer
}

func NewFileSource(path string) *FileSource {
	return &FileSource{Path: path, Client: &http.Client{Timeout: requestTimeout}}
}

func (s *FileSource) Close() error { return nil }

func (s *FileSource) fetch(ctx context.Context, endpoint, name string, auth credentials) (map[string]any, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("could not prepare ChatGPT %s request", name)
	}
	request.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	request.Header.Set("ChatGPT-Account-Id", auth.AccountID)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("OAI-Product-Sku", "codex")
	request.Header.Set("User-Agent", "claudex-limits/1")
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("ChatGPT %s request timed out", name)
		}
		return nil, fmt.Errorf("could not reach ChatGPT for %s", name)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, errors.New("ChatGPT rejected the saved OAuth token; run a Codex request to refresh it")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("ChatGPT %s request failed with HTTP %d", name, response.StatusCode)
	}
	var result map[string]any
	decoder := json.NewDecoder(io.LimitReader(response.Body, 8<<20))
	if err := decoder.Decode(&result); err != nil || result == nil {
		return nil, fmt.Errorf("ChatGPT returned an invalid %s response", name)
	}
	return result, nil
}

func (s *FileSource) Read() (Snapshot, error) {
	auth, err := readCredentials(s.Path)
	if err != nil {
		return Snapshot{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	usage, err := s.fetch(ctx, usageURL, "usage", auth)
	if err != nil {
		return Snapshot{}, err
	}
	now := time.Now()
	snapshot := NormalizeUsage(usage, now)
	// Reset-credit details are optional. The usage response remains useful when
	// the endpoint is absent, capped, or unavailable for an account.
	details, detailErr := s.fetch(ctx, resetCreditsURL, "reset-credit", auth)
	if detailErr == nil {
		base := snapshot.BankedResets
		detail := normalizeCredits(details, now)
		snapshot.BankedResets = mergeCreditDetails(base, detail, details, now)
	} else if snapshot.BankedResets != nil && snapshot.BankedResets.Available != nil {
		snapshot.BankedResets.ExpirationDetailsPartial = true
	}
	return snapshot, nil
}

type LoginRequiredError struct{ Message string }

func (e *LoginRequiredError) Error() string { return e.Message }

var errInterrupted = errors.New("interrupted")

type rpcMessage struct {
	ID     json.RawMessage
	Result json.RawMessage
	Error  json.RawMessage
}

type appServerSource struct {
	command   *exec.Cmd
	stdin     io.WriteCloser
	messages  chan rpcMessage
	readerErr chan error
	writeMu   sync.Mutex
	idMu      sync.Mutex
	nextID    uint64
	closeOnce sync.Once
	readDone  chan struct{}
	closed    chan struct{}
	planType  string
}

func codexExecutable() string {
	for _, name := range []string{"CLAUDEX_LIMITS_CODEX_BIN", "CODEX_LIMITS_CODEX_BIN"} {
		if override := strings.TrimSpace(os.Getenv(name)); override != "" {
			return override
		}
	}
	return "codex"
}

func NewAppServerSource() (*appServerSource, error) {
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupts)
	command := exec.Command(codexExecutable(), "app-server")
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, errors.New("could not start the Codex app server")
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, errors.New("could not start the Codex app server")
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return nil, errors.New("Codex CLI is not available; install it or use --auth-file")
	}
	source := &appServerSource{command: command, stdin: stdin, messages: make(chan rpcMessage, 16), readerErr: make(chan error, 1), readDone: make(chan struct{}), closed: make(chan struct{})}
	go source.readMessages(stdout)
	if _, err := source.request("initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "claudex-limits", "title": "claudex-limits", "version": version},
		"capabilities": map[string]any{},
	}, 10*time.Second, interrupts); err != nil {
		source.Close()
		return nil, err
	}
	if err := source.sendNotification("initialized", map[string]any{}); err != nil {
		source.Close()
		return nil, err
	}
	account, err := source.request("account/read", map[string]any{"refreshToken": true}, requestTimeout, interrupts)
	if err != nil {
		source.Close()
		return nil, err
	}
	if !hasChatGPTAccount(account) {
		source.Close()
		return nil, &LoginRequiredError{Message: "no ChatGPT account is logged in; run `codex login`"}
	}
	if accountObject := objectField(account, "account"); accountObject != nil {
		source.planType = stringField(accountObject, "planType", "plan_type")
	}
	if source.planType == "" {
		source.planType = stringField(account, "planType", "plan_type")
	}
	return source, nil
}

func (s *appServerSource) readMessages(stdout io.Reader) {
	defer close(s.readDone)
	decoder := json.NewDecoder(bufio.NewReader(stdout))
	for {
		var raw map[string]json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			if !errors.Is(err, io.EOF) {
				s.readerErr <- errors.New("Codex app server returned invalid data")
			}
			close(s.messages)
			return
		}
		id := raw["id"]
		if len(id) == 0 {
			continue
		}
		select {
		case s.messages <- rpcMessage{ID: id, Result: raw["result"], Error: raw["error"]}:
		case <-s.closed:
			return
		}
	}
}

func (s *appServerSource) sendNotification(method string, params any) error {
	return s.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (s *appServerSource) send(request map[string]any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.stdin == nil {
		return errors.New("Codex app server is closed")
	}
	data, err := json.Marshal(request)
	if err != nil {
		return errors.New("could not encode Codex app-server request")
	}
	data = append(data, '\n')
	if _, err := s.stdin.Write(data); err != nil {
		return errors.New("Codex app server stopped")
	}
	return nil
}

func (s *appServerSource) request(method string, params any, timeout time.Duration, interrupts ...<-chan os.Signal) (map[string]any, error) {
	s.idMu.Lock()
	s.nextID++
	id := s.nextID
	s.idMu.Unlock()
	if err := s.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	idText := fmt.Sprintf("%d", id)
	var interrupted <-chan os.Signal
	if len(interrupts) > 0 {
		interrupted = interrupts[0]
	}
	for {
		select {
		case <-interrupted:
			return nil, errInterrupted
		case message, ok := <-s.messages:
			if !ok {
				select {
				case err := <-s.readerErr:
					return nil, err
				default:
					return nil, errors.New("Codex app server stopped")
				}
			}
			if string(bytes.TrimSpace(message.ID)) != idText {
				continue
			}
			if len(message.Error) != 0 && string(message.Error) != "null" {
				return nil, fmt.Errorf("Codex app-server %s request failed", method)
			}
			if len(message.Result) == 0 || string(message.Result) == "null" {
				return map[string]any{}, nil
			}
			var result map[string]any
			if err := json.Unmarshal(message.Result, &result); err != nil || result == nil {
				return nil, fmt.Errorf("Codex app-server %s returned an invalid response", method)
			}
			return result, nil
		case <-s.readerErr:
			return nil, errors.New("Codex app server stopped")
		case <-deadline.C:
			return nil, fmt.Errorf("Codex app-server %s request timed out", method)
		}
	}
}

func hasChatGPTAccount(result map[string]any) bool {
	account, exists := result["account"]
	if !exists || account == nil {
		return false
	}
	if object, ok := account.(map[string]any); ok {
		typeName := strings.ToLower(stringField(object, "type", "accountType", "account_type"))
		if strings.Contains(typeName, "apikey") || strings.Contains(typeName, "api_key") {
			return false
		}
		if typeName == "" || strings.Contains(typeName, "chatgpt") || strings.Contains(typeName, "oauth") {
			return true
		}
		return false
	}
	if typeName, ok := account.(string); ok {
		return strings.Contains(strings.ToLower(typeName), "chatgpt")
	}
	return false
}

func (s *appServerSource) Read() (Snapshot, error) {
	result, err := s.request("account/rateLimits/read", map[string]any{}, requestTimeout)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := NormalizeUsage(result, time.Now())
	if snapshot.PlanType == "" {
		snapshot.PlanType = s.planType
	}
	return snapshot, nil
}

func (s *appServerSource) Close() error {
	var result error
	s.closeOnce.Do(func() {
		close(s.closed)
		if s.stdin != nil {
			_ = s.stdin.Close()
		}
		if s.command != nil && s.command.Process != nil {
			// Closing stdin lets a cooperative app server exit. Kill before Wait
			// if it does not, so this exact child is always reaped promptly.
			if s.command.ProcessState == nil {
				_ = s.command.Process.Kill()
			}
			if err := s.command.Wait(); err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					result = err
				}
			}
		}
		select {
		case <-s.readDone:
		case <-time.After(time.Second):
			result = errors.New("Codex app server reader did not stop")
		}
	})
	return result
}

type demoSource struct {
	started time.Time
}

func NewDemoSource() Source { return &demoSource{started: time.Now()} }

func (s *demoSource) Close() error { return nil }

func (s *demoSource) Read() (Snapshot, error) {
	elapsed := time.Since(s.started).Seconds()
	primary := 64 + 12*math.Sin(elapsed/18)
	weekly := 38 + 8*math.Sin(elapsed/35+1)
	raw := map[string]any{"planType": "demo", "rateLimits": map[string]any{
		"primary":   map[string]any{"usedPercent": 100 - primary, "windowDurationMins": 300, "resetsAt": time.Now().Add(2 * time.Hour).Format(time.RFC3339)},
		"secondary": map[string]any{"usedPercent": 100 - weekly, "windowDurationMins": 10080, "resetsAt": time.Now().Add(4 * 24 * time.Hour).Format(time.RFC3339)},
	}, "rateLimitResetCredits": map[string]any{"availableCount": 2, "applicableAvailableCount": 1}}
	snapshot := NormalizeUsage(raw, time.Now())
	return snapshot, nil
}

func OpenSource(source, authFile string, explicitAuth bool) (Source, error) {
	if source == "claude" {
		if explicitAuth && authFile == "" {
			return nil, errors.New("--auth-file requires a path")
		}
		return OpenClaudeSource(authFile)
	}
	if (source == "auto" || source == "") && authFile == "" && !explicitAuth {
		return openAutoSource()
	}
	return openCodexSource(source, authFile, explicitAuth)
}

func openCodexSource(source, authFile string, explicitAuth bool) (Source, error) {
	if explicitAuth || authFile != "" {
		if source != "" && source != "auto" {
			return nil, errors.New("--auth-file cannot be combined with --source codex or --source proxy")
		}
		if authFile == "" {
			return nil, errors.New("--auth-file requires a path")
		}
		return NewFileSource(filepath.Clean(expandPath(authFile))), nil
	}
	switch source {
	case "proxy":
		path, err := findProxyAuth()
		if err != nil {
			return nil, err
		}
		return NewFileSource(path), nil
	case "codex":
		return NewAppServerSource()
	case "", "auto":
		if _, err := exec.LookPath(codexExecutable()); err == nil {
			app, appErr := NewAppServerSource()
			if appErr == nil {
				return app, nil
			}
			var loginErr *LoginRequiredError
			if !errors.As(appErr, &loginErr) {
				return nil, appErr
			}
		}
		if path, ok := findNativeAuth(); ok {
			if _, err := readCredentials(path); err == nil {
				return NewFileSource(path), nil
			}
		}
		path, err := findProxyAuth()
		if err != nil {
			return nil, &LoginRequiredError{Message: "no ChatGPT account is available; run `codex login` or provide --auth-file"}
		}
		return NewFileSource(path), nil
	default:
		return nil, fmt.Errorf("invalid account source %q", source)
	}
}
