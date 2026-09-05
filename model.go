package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	FiveHourSeconds = int64(5 * 60 * 60)
	WeekSeconds     = int64(7 * 24 * 60 * 60)
	HistorySeconds  = int64(4 * 60 * 60)
)

// Reset describes when a quota window or banked reset becomes available.
type Reset struct {
	AfterSeconds int64   `json:"after_seconds"`
	At           string  `json:"at,omitempty"`
	Timestamp    float64 `json:"timestamp"`
}

type GenericWindow struct {
	Name             string   `json:"name"`
	RemainingPercent *float64 `json:"remaining_percent,omitempty"`
	WindowSeconds    int64    `json:"window_seconds,omitempty"`
	Reset            *Reset   `json:"reset,omitempty"`
}

type AdditionalLimit struct {
	Name                     string   `json:"name"`
	FiveHourRemainingPercent *float64 `json:"five_hour_remaining_percent,omitempty"`
	FiveHourReset            *Reset   `json:"five_hour_reset,omitempty"`
	WeeklyRemainingPercent   *float64 `json:"weekly_remaining_percent,omitempty"`
	WeeklyReset              *Reset   `json:"weekly_reset,omitempty"`
}

type Expiration struct {
	AfterSeconds int64   `json:"after_seconds"`
	At           string  `json:"at,omitempty"`
	Timestamp    float64 `json:"timestamp"`
}

type BankedResets struct {
	Available                *int64       `json:"available,omitempty"`
	Applicable               *int64       `json:"applicable,omitempty"`
	Expirations              []Expiration `json:"expirations,omitempty"`
	ExpirationDetailsPartial bool         `json:"expiration_details_partial,omitempty"`
}

// Snapshot is the stable JSON format emitted by codex-limits. The named
// five-hour and weekly fields preserve the original CLI format. Windows holds
// every quota window, including other durations and buckets.
type Snapshot struct {
	FiveHourRemainingPercent *float64          `json:"five_hour_remaining_percent,omitempty"`
	FiveHourReset            *Reset            `json:"five_hour_reset,omitempty"`
	WeeklyRemainingPercent   *float64          `json:"weekly_remaining_percent,omitempty"`
	WeeklyReset              *Reset            `json:"weekly_reset,omitempty"`
	AdditionalRateLimits     []AdditionalLimit `json:"additional_rate_limits"`
	BankedResets             *BankedResets     `json:"banked_resets,omitempty"`
	Windows                  []GenericWindow   `json:"windows"`
	PlanType                 string            `json:"plan_type,omitempty"`
}

func floatPtr(v float64) *float64 { return &v }
func intPtr(v int64) *int64       { return &v }

func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func numeric(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, isFinite(v)
	case float32:
		return float64(v), isFinite(float64(v))
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil && isFinite(f)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil && isFinite(f)
	default:
		return 0, false
	}
}

func numberField(object map[string]any, names ...string) (float64, bool) {
	for _, name := range names {
		if value, ok := object[name]; ok {
			return numeric(value)
		}
	}
	return 0, false
}

func stringField(object map[string]any, names ...string) string {
	for _, name := range names {
		if value, ok := object[name].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func objectField(object map[string]any, names ...string) map[string]any {
	for _, name := range names {
		if value, ok := object[name].(map[string]any); ok {
			return value
		}
	}
	return nil
}

func arrayField(object map[string]any, names ...string) []any {
	for _, name := range names {
		if value, ok := object[name].([]any); ok {
			return value
		}
	}
	return nil
}

func parseTimestamp(value any) (time.Time, bool) {
	if number, ok := numeric(value); ok {
		// API timestamps are seconds. Be forgiving of millisecond values from
		// third-party proxy implementations.
		if number > 1e12 {
			number /= 1000
		}
		if number > 0 && number < 1e12 {
			return time.Unix(int64(number), int64((number-float64(int64(number)))*1e9)), true
		}
	}
	text, ok := value.(string)
	if !ok {
		return time.Time{}, false
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000-07:00", "2006-01-02 15:04:05Z07:00"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func makeReset(value any, now time.Time) *Reset {
	when, ok := parseTimestamp(value)
	if !ok {
		return nil
	}
	seconds := int64(when.Sub(now).Seconds())
	if seconds < 0 {
		seconds = 0
	}
	return &Reset{AfterSeconds: seconds, At: when.Local().Format(time.RFC3339), Timestamp: float64(when.UnixNano()) / 1e9}
}

func durationLabel(seconds int64) string {
	switch seconds {
	case FiveHourSeconds:
		return "5h"
	case WeekSeconds:
		return "Weekly"
	}
	if seconds <= 0 {
		return "unknown window"
	}
	minutes := seconds / 60
	if minutes%60 == 0 {
		return fmt.Sprintf("%dh", minutes/60)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%ds", seconds)
}

type rawWindow struct {
	Remaining *float64
	Seconds   int64
	Reset     *Reset
}

func normalizeWindow(raw map[string]any, now time.Time) (rawWindow, bool) {
	used, hasUsed := numberField(raw, "usedPercent", "used_percent")
	seconds, hasSeconds := numberField(raw, "windowDurationMins", "window_duration_mins")
	if !hasSeconds {
		seconds, hasSeconds = numberField(raw, "limit_window_seconds", "windowDurationSeconds", "window_duration_seconds")
	} else {
		seconds *= 60
	}
	if hasSeconds && (seconds <= 0 || seconds > float64(math.MaxInt64)) {
		return rawWindow{}, false
	}
	result := rawWindow{Seconds: int64(seconds)}
	if hasUsed && used >= 0 && used <= 100 {
		result.Remaining = floatPtr(100 - used)
	}
	if resetValue, ok := raw["resetsAt"]; ok {
		result.Reset = makeReset(resetValue, now)
	} else if resetValue, ok := raw["reset_at"]; ok {
		result.Reset = makeReset(resetValue, now)
	}
	if result.Remaining == nil && result.Reset == nil {
		return rawWindow{}, false
	}
	return result, true
}

type rawBucket struct {
	ID        string
	Name      string
	Primary   map[string]any
	Secondary map[string]any
	Raw       map[string]any
}

func bucketWindows(bucket rawBucket, now time.Time, primary bool) []GenericWindow {
	result := make([]GenericWindow, 0, 2)
	for _, pair := range []struct {
		kind string
		raw  map[string]any
	}{{"primary", bucket.Primary}, {"secondary", bucket.Secondary}} {
		if pair.raw == nil {
			continue
		}
		window, ok := normalizeWindow(pair.raw, now)
		if !ok {
			continue
		}
		label := durationLabel(window.Seconds)
		if window.Seconds == 0 {
			label = "Primary"
			if pair.kind == "secondary" {
				label = "Secondary"
			}
		}
		if !primary || bucket.Name != "" {
			prefix := bucket.Name
			if prefix == "" {
				prefix = bucket.ID
			}
			if prefix != "" {
				label = prefix + " / " + label
			}
		}
		result = append(result, GenericWindow{Name: label, RemainingPercent: window.Remaining, WindowSeconds: window.Seconds, Reset: window.Reset})
	}
	return result
}

func mapRaw(raw any) map[string]any {
	value, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	return value
}

func rawBuckets(data map[string]any) []rawBucket {
	legacy := objectField(data, "rateLimits", "rate_limits", "rate_limit")
	byID := objectField(data, "rateLimitsByLimitId", "rate_limits_by_limit_id")
	var result []rawBucket
	primaryID := ""
	if legacy != nil {
		primaryID = stringField(legacy, "limitId", "limit_id")
		result = append(result, rawBucket{ID: primaryID, Name: "", Primary: objectField(legacy, "primary", "primary_window"), Secondary: objectField(legacy, "secondary", "secondary_window"), Raw: legacy})
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if legacy == nil && len(ids) > 0 {
		// App-server responses may omit legacy rateLimits. Codex is the
		// established primary ID; only use lexical order as a final fallback.
		primaryID = ids[0]
		for _, id := range ids {
			if strings.EqualFold(id, "codex") {
				primaryID = id
				break
			}
		}
		bucket := mapRaw(byID[primaryID])
		if bucket != nil {
			result = append(result, rawBucket{ID: primaryID, Name: "", Primary: objectField(bucket, "primary", "primary_window"), Secondary: objectField(bucket, "secondary", "secondary_window"), Raw: bucket})
		}
	}
	for _, id := range ids {
		if id == primaryID {
			continue
		}
		bucket := mapRaw(byID[id])
		if bucket == nil {
			continue
		}
		// The legacy field is the primary bucket. Skip the same ID and also
		// skip structurally identical data from proxies that omit limitId.
		if id == primaryID || (legacy != nil && sameJSON(bucket, legacy)) {
			continue
		}
		name := stringField(bucket, "limitName", "limit_name", "meteredFeature", "metered_feature")
		result = append(result, rawBucket{ID: id, Name: name, Primary: objectField(bucket, "primary", "primary_window"), Secondary: objectField(bucket, "secondary", "secondary_window"), Raw: bucket})
	}
	for _, item := range arrayField(data, "additional_rate_limits", "additionalRateLimits") {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		bucket := objectField(object, "rate_limit", "rateLimit")
		if bucket == nil {
			continue
		}
		name := stringField(object, "limit_name", "limitName", "metered_feature", "meteredFeature")
		result = append(result, rawBucket{ID: name, Name: name, Primary: objectField(bucket, "primary", "primary_window"), Secondary: objectField(bucket, "secondary", "secondary_window"), Raw: bucket})
	}
	return result
}

func sameJSON(a, b map[string]any) bool {
	left, errA := json.Marshal(a)
	right, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(left) == string(right)
}

func normalizeCredits(data map[string]any, now time.Time) *BankedResets {
	summary := objectField(data, "rateLimitResetCredits", "rate_limit_reset_credits")
	if summary == nil && (data["availableCount"] != nil || data["available_count"] != nil || data["credits"] != nil) {
		summary = data
	}
	if summary == nil {
		return nil
	}
	result := &BankedResets{}
	if available, ok := numberField(summary, "availableCount", "available_count"); ok && available >= 0 && available == math.Trunc(available) {
		result.Available = intPtr(int64(available))
	}
	if applicable, ok := numberField(summary, "applicableAvailableCount", "applicable_available_count"); ok && applicable >= 0 && applicable == math.Trunc(applicable) {
		result.Applicable = intPtr(int64(applicable))
	}
	credits := arrayField(summary, "credits")
	if credits == nil {
		// The direct endpoint returns the same object, while some versions put
		// details under a separate response object. The caller merges that data.
		result.ExpirationDetailsPartial = result.Available != nil && *result.Available > 0
	} else {
		for _, item := range credits {
			credit, ok := item.(map[string]any)
			if !ok || strings.ToLower(stringField(credit, "status")) != "available" {
				continue
			}
			when, ok := parseTimestamp(credit["expiresAt"])
			if !ok {
				when, ok = parseTimestamp(credit["expires_at"])
			}
			if !ok {
				result.ExpirationDetailsPartial = true
				continue
			}
			seconds := int64(when.Sub(now).Seconds())
			if seconds < 0 {
				seconds = 0
			}
			result.Expirations = append(result.Expirations, Expiration{AfterSeconds: seconds, At: when.Local().Format(time.RFC3339), Timestamp: float64(when.UnixNano()) / 1e9})
		}
		if result.Available != nil && int64(len(result.Expirations)) != *result.Available {
			result.ExpirationDetailsPartial = true
		}
	}
	sort.Slice(result.Expirations, func(i, j int) bool { return result.Expirations[i].Timestamp < result.Expirations[j].Timestamp })
	if result.Available == nil && result.Applicable == nil && len(result.Expirations) == 0 {
		return nil
	}
	return result
}

func mergeCreditDetails(base, detail *BankedResets, detailData map[string]any, now time.Time) *BankedResets {
	if base == nil {
		base = normalizeCredits(detailData, now)
	}
	if base == nil {
		return detail
	}
	if detail == nil {
		return base
	}
	detailContainer := objectField(detailData, "rateLimitResetCredits", "rate_limit_reset_credits")
	if detailContainer == nil && (detailData["availableCount"] != nil || detailData["available_count"] != nil || detailData["credits"] != nil) {
		detailContainer = detailData
	}
	if detailContainer != nil {
		// Details are authoritative when present. Recompute the completeness
		// flag from this response so a complete uncapped response clears the
		// usage summary's earlier partial state.
		base.ExpirationDetailsPartial = detail.ExpirationDetailsPartial
	}
	if detail.Available != nil {
		base.Available = detail.Available
	}
	if detail.Applicable != nil {
		base.Applicable = detail.Applicable
	}
	if len(detail.Expirations) > 0 {
		base.Expirations = detail.Expirations
	}
	base.ExpirationDetailsPartial = base.ExpirationDetailsPartial || detail.ExpirationDetailsPartial
	if base.Available != nil && int64(len(base.Expirations)) != *base.Available {
		base.ExpirationDetailsPartial = true
	}
	return base
}

// NormalizeUsage converts either the app-server camelCase response or the
// legacy direct HTTP response to the stable Snapshot format.
func NormalizeUsage(data map[string]any, now time.Time) Snapshot {
	result := Snapshot{AdditionalRateLimits: []AdditionalLimit{}, Windows: []GenericWindow{}}
	if plan := stringField(data, "planType", "plan_type"); plan != "" {
		result.PlanType = plan
	} else if account := objectField(data, "account"); account != nil {
		result.PlanType = stringField(account, "planType", "plan_type")
	}
	buckets := rawBuckets(data)
	for bucketIndex, bucket := range buckets {
		windows := bucketWindows(bucket, now, bucketIndex == 0)
		result.Windows = append(result.Windows, windows...)
		if bucketIndex == 0 {
			for _, window := range windows {
				switch window.WindowSeconds {
				case FiveHourSeconds:
					result.FiveHourRemainingPercent, result.FiveHourReset = window.RemainingPercent, window.Reset
				case WeekSeconds:
					result.WeeklyRemainingPercent, result.WeeklyReset = window.RemainingPercent, window.Reset
				}
			}
			continue
		}
		additional := AdditionalLimit{Name: bucket.Name}
		if additional.Name == "" {
			additional.Name = bucket.ID
		}
		if additional.Name == "" {
			additional.Name = "Additional limit"
		}
		for _, window := range windows {
			switch window.WindowSeconds {
			case FiveHourSeconds:
				additional.FiveHourRemainingPercent, additional.FiveHourReset = window.RemainingPercent, window.Reset
			case WeekSeconds:
				additional.WeeklyRemainingPercent, additional.WeeklyReset = window.RemainingPercent, window.Reset
			}
		}
		if additional.FiveHourRemainingPercent != nil || additional.FiveHourReset != nil || additional.WeeklyRemainingPercent != nil || additional.WeeklyReset != nil {
			result.AdditionalRateLimits = append(result.AdditionalRateLimits, additional)
		}
	}
	result.BankedResets = normalizeCredits(data, now)
	return result
}

// NormalizeCodex is an alias retained for callers that used the Python
// prototype's terminology.
func NormalizeCodex(data map[string]any, now time.Time) Snapshot { return NormalizeUsage(data, now) }
