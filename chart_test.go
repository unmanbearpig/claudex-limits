package main

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var coloredBraille = regexp.MustCompile(`\x1b\[38;5;([0-9]+)m[\x{2801}-\x{28ff}]`)
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestChartSharedCellsAlternateColorsAndPreserveDots(t *testing.T) {
	end := time.Unix(1_800_000_000, 0)
	for _, test := range []struct {
		name   string
		names  []string
		values map[string]float64
	}{
		{"identical", []string{"Claude / 5h", "Codex / 5h"}, map[string]float64{"Claude / 5h": 100, "Codex / 5h": 100}},
		{"nearby", []string{"Claude / Weekly", "Codex / Weekly"}, map[string]float64{"Claude / Weekly": 92, "Codex / Weekly": 93}},
		{"four lines", []string{"Claude / 5h", "Claude / Weekly", "Codex / 5h", "Codex / Weekly"}, map[string]float64{"Claude / 5h": 100, "Claude / Weekly": 100, "Codex / 5h": 100, "Codex / Weekly": 100}},
	} {
		t.Run(test.name, func(t *testing.T) {
			history := []Sample{
				{At: end.Add(-time.Duration(HistorySeconds) * time.Second), Values: test.values, Synthetic: true},
				{At: end, Values: test.values, Synthetic: true},
			}
			lines := ChartLines(history, end, 24, 8, test.names, true)
			output := strings.Join(lines, "\n")
			matches := coloredBraille.FindAllStringSubmatch(output, -1)
			if len(matches) != 24 {
				t.Fatalf("got %d plotted cells, want 24", len(matches))
			}
			shades := seriesColors(test.names, "all")
			counts := make(map[int]int)
			for index, match := range matches {
				shade, _ := strconv.Atoi(match[1])
				counts[shade]++
				if index > 0 && match[1] == matches[index-1][1] {
					t.Fatalf("adjacent shared cells use the same color: %s", output)
				}
			}
			for _, shade := range shades {
				if counts[shade] != 24/len(shades) {
					t.Fatalf("series color %d appears %d times, want %d", shade, counts[shade], 24/len(shades))
				}
			}
			plain := strings.Join(ChartLines(history, end, 24, 8, test.names, false), "\n")
			if ansiEscape.ReplaceAllString(output, "") != plain {
				t.Fatal("alternating colors changed the plotted dots")
			}
			if strings.Join(ChartLines(history, end, 24, 8, test.names, true), "\n") != output {
				t.Fatal("shared-cell colors changed between identical renders")
			}
		})
	}
}

func TestChartSeparateLinesKeepTheirOwnColors(t *testing.T) {
	end := time.Unix(1_800_000_000, 0)
	names := []string{"Claude / 5h", "Codex / 5h"}
	values := map[string]float64{names[0]: 100, names[1]: 0}
	history := []Sample{
		{At: end.Add(-time.Duration(HistorySeconds) * time.Second), Values: values, Synthetic: true},
		{At: end, Values: values, Synthetic: true},
	}
	lines := ChartLines(history, end, 24, 8, names, true)
	for index, row := range []int{1, 8} {
		matches := coloredBraille.FindAllStringSubmatch(lines[row], -1)
		if len(matches) != 24 {
			t.Fatalf("line %d has %d cells, want 24", index, len(matches))
		}
		for _, match := range matches {
			if match[1] != strconv.Itoa(seriesColors(names, "all")[index]) {
				t.Fatalf("separate line %d changed color: %s", index, lines[row])
			}
		}
	}
}

func TestDashboardLabelsMatchLegendAndFitTerminal(t *testing.T) {
	t.Setenv("COLUMNS", "72")
	t.Setenv("LINES", "24")
	now := time.Unix(1_800_000_000, 0)
	names := []string{"Claude / 5h", "Claude / Weekly", "Codex / 5h", "Codex / Weekly"}
	values := map[string]float64{names[0]: 100, names[1]: 92, names[2]: 100, names[3]: 93}
	snapshot := Snapshot{Provider: "all"}
	for _, name := range names {
		snapshot.Windows = append(snapshot.Windows, GenericWindow{Name: name, RemainingPercent: floatPtr(values[name])})
	}
	history := []Sample{{At: now, Values: values}}
	for _, color := range []bool{false, true} {
		var output strings.Builder
		dashboard(&output, io.Discard, history, &snapshot, 5, now, "", color, now)
		plain := ansiEscape.ReplaceAllString(output.String(), "")
		for index, name := range names {
			if !strings.Contains(plain, fmt.Sprintf("[%d] ━━  %s", index+1, name)) {
				t.Fatalf("missing numbered legend for %s: %s", name, plain)
			}
		}
		if !strings.Contains(plain, "│ [1][3][4]") {
			t.Fatalf("overlapping session lines lack both endpoint labels: %s", plain)
		}
		if !strings.Contains(plain, "│ [2]\n") {
			t.Fatalf("Claude weekly label does not match its chart row: %s", plain)
		}
		for _, line := range strings.Split(plain, "\n") {
			if len([]rune(line)) >= 72 {
				t.Fatalf("frame line exceeds the terminal width: %q", line)
			}
		}
	}
}

func TestNimbusQuillHiddenByDefaultIncludingSavedHistory(t *testing.T) {
	t.Setenv("COLUMNS", "120")
	t.Setenv("LINES", "32")
	now := time.Unix(1_800_000_000, 0)
	for _, combined := range []bool{false, true} {
		provider, prefix := "claude", ""
		if combined {
			provider, prefix = "all", "Claude / "
		}
		for _, available := range []bool{false, true} {
			name := prefix + "Nimbus Quill / Weekly"
			snapshot := Snapshot{Provider: provider, Windows: []GenericWindow{{Name: prefix + "5h", RemainingPercent: floatPtr(75)}}}
			if available {
				snapshot.Windows = append(snapshot.Windows, GenericWindow{Name: name, RemainingPercent: floatPtr(50)})
			}
			history := []Sample{{At: now.Add(-5 * time.Second), Values: map[string]float64{name: 50}}, {At: now, Values: snapshotValues(snapshot)}}
			var output strings.Builder
			dashboard(&output, io.Discard, history, &snapshot, 5, now, "", false, now)
			if strings.Contains(output.String(), "Nimbus Quill") || !strings.Contains(output.String(), prefix+"5h") {
				t.Fatalf("default chart shows Nimbus Quill or hides another line: %s", &output)
			}
			output.Reset()
			dashboard(&output, io.Discard, history, &snapshot, 5, now, "", false, now, displayOptions{showNimbusQuill: true})
			if !strings.Contains(output.String(), name) {
				t.Fatalf("opt-in did not restore saved Nimbus Quill series: %s", &output)
			}
			if !available && !strings.Contains(output.String(), "unavailable") {
				t.Fatal("opt-in invented a current Nimbus Quill value")
			}
		}
	}
}

func TestNimbusQuillTextOptInPreservesJSON(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	snapshot := NormalizeClaudeUsage(map[string]any{
		"five_hour":              map[string]any{"utilization": 25},
		"nimbus_quill":           map[string]any{"utilization": 50},
		"seven_day_nimbus_quill": map[string]any{"utilization": 60},
		"seven_day_sonnet":       map[string]any{"utilization": 10},
	}, now)
	for _, combined := range []bool{false, true} {
		current := snapshot
		if combined {
			current = Snapshot{Provider: "all", Accounts: []AccountSnapshot{{Provider: "claude", Snapshot: &snapshot}}}
		}
		var output strings.Builder
		PrintSnapshot(&output, current)
		if strings.Contains(output.String(), "Nimbus Quill") || !strings.Contains(output.String(), "Sonnet") {
			t.Fatalf("default text shows Nimbus Quill or hides another bucket: %s", &output)
		}
		output.Reset()
		PrintSnapshot(&output, current, displayOptions{showNimbusQuill: true})
		if !strings.Contains(output.String(), "Nimbus Quill left:") || !strings.Contains(output.String(), "Nimbus Quill:") {
			t.Fatalf("opt-in did not restore both Nimbus Quill windows: %s", &output)
		}
		data, err := json.Marshal(current)
		if err != nil || !strings.Contains(string(data), "Nimbus Quill") {
			t.Fatalf("display filtering removed Nimbus Quill from JSON: %s, %v", data, err)
		}
	}
	opts, err := parseOptions([]string{"--show-nimbus-quill", "--live"})
	if err != nil || !opts.showNimbusQuill {
		t.Fatalf("opt-in flag was not accepted: %#v, %v", opts, err)
	}
}
