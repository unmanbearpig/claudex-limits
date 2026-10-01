package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	codexColor  = 75
	claudeColor = 208
)

var palette = []int{codexColor, 111, 69, 39, 33, 153}

type displayOptions struct {
	showNimbusQuill bool
}

func (opts displayOptions) visible(name, provider string) bool {
	if opts.showNimbusQuill {
		return true
	}
	if strings.HasPrefix(name, "Claude / ") {
		name = strings.TrimPrefix(name, "Claude / ")
	} else if provider != "claude" {
		return true
	}
	bucket, _, _ := strings.Cut(name, " / ")
	return !strings.EqualFold(bucket, "Nimbus Quill")
}

func providerColor(provider string) int {
	if provider == "claude" {
		return claudeColor
	}
	return codexColor
}

func seriesColors(names []string, provider string) []int {
	colors := make([]int, len(names))
	codexIndex, claudeIndex := 0, 0
	for i, name := range names {
		if provider == "claude" || strings.HasPrefix(name, "Claude / ") {
			base := strings.TrimPrefix(name, "Claude / ")
			switch base {
			case "5h":
				colors[i] = 166 // Darker orange keeps the session below weekly in prominence.
			case "Weekly":
				colors[i] = 214
			default:
				shades := []int{209, 215, 216, 202, 173}
				colors[i] = shades[claudeIndex%len(shades)]
				bucket, _, _ := strings.Cut(base, " / ")
				if strings.EqualFold(bucket, "Nimbus Quill") {
					colors[i] = 94 // Keep this opaque provider bucket subdued.
				}
				claudeIndex++
			}
		} else {
			colors[i] = palette[codexIndex%len(palette)]
			codexIndex++
		}
	}
	return colors
}

var brailleBits = [2][4]byte{{1, 2, 4, 64}, {8, 16, 32, 128}}

type Sample struct {
	At        time.Time
	Values    map[string]float64
	Synthetic bool
}

func colorize(text string, shade int, enabled bool) string {
	if !enabled {
		return text
	}
	return fmt.Sprintf("\x1b[38;5;%dm%s\x1b[0m", shade, text)
}

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// ChartLines rasterizes samples into a fixed four-hour Braille chart. Empty
// Values maps create gaps and never connect to a later successful sample.
func ChartLines(history []Sample, end time.Time, width, height int, names []string, color bool, intervals ...float64) []string {
	return chartLines(history, end, width, height, names, color, seriesColors(names, ""), intervals...)
}

func chartLines(history []Sample, end time.Time, width, height int, names []string, color bool, colors []int, intervals ...float64) []string {
	width = clampInt(width, 8, 240)
	height = clampInt(height, 5, 30)
	if end.IsZero() {
		for index := len(history) - 1; index >= 0; index-- {
			if !history[index].At.IsZero() {
				end = history[index].At
				break
			}
		}
		if end.IsZero() {
			end = time.Now()
		}
	}
	start := end.Add(-time.Duration(HistorySeconds) * time.Second)
	pixelsX, pixelsY := width*2, height*4
	masks := make([][]byte, height)
	owners := make([][]map[int]struct{}, height)
	for row := range masks {
		masks[row] = make([]byte, width)
		owners[row] = make([]map[int]struct{}, width)
	}
	dot := func(x, y, series int) {
		if x < 0 || y < 0 || x >= pixelsX || y >= pixelsY {
			return
		}
		row, column := y/4, x/2
		masks[row][column] |= brailleBits[x%2][y%4]
		if owners[row][column] == nil {
			owners[row][column] = make(map[int]struct{})
		}
		owners[row][column][series] = struct{}{}
	}
	gapThreshold := 10 * time.Second
	if len(intervals) > 0 && intervals[0] > 0 && isFinite(intervals[0]) {
		gapThreshold = time.Duration(intervals[0] * 1.8 * float64(time.Second))
	}
	for series, name := range names {
		var previousX, previousY int
		var previousAt time.Time
		previousSynthetic := false
		hasPrevious := false
		for _, sample := range history {
			value, ok := sample.Values[name]
			if !ok || sample.At.Before(start) || sample.At.After(end) {
				hasPrevious = false
				continue
			}
			if value < 0 {
				value = 0
			}
			if value > 100 {
				value = 100
			}
			x := int((sample.At.Sub(start).Seconds()/float64(HistorySeconds))*float64(pixelsX-1) + 0.5)
			y := int(((100-value)/100)*float64(pixelsY-1) + 0.5)
			dot(x, y, series)
			if hasPrevious && (sample.At.Sub(previousAt) <= gapThreshold || (sample.Synthetic && previousSynthetic)) {
				steps := previousX - x
				if steps < 0 {
					steps = -steps
				}
				deltaY := previousY - y
				if deltaY < 0 {
					deltaY = -deltaY
				}
				if deltaY > steps {
					steps = deltaY
				}
				if steps < 1 {
					steps = 1
				}
				for step := 0; step <= steps; step++ {
					dot(previousX+(x-previousX)*step/steps, previousY+(y-previousY)*step/steps, series)
				}
			}
			previousX, previousY, previousAt, previousSynthetic, hasPrevious = x, y, sample.At, sample.Synthetic, true
		}
	}
	ticks := map[int]string{}
	for _, fraction := range []float64{0, .25, .5, .75, 1} {
		row := int(fraction*float64(height-1) + .5)
		ticks[row] = fmt.Sprintf("%3.0f%% ", 100-fraction*100)
	}
	verticals := map[int]struct{}{}
	for _, fraction := range []float64{.25, .5, .75} {
		verticals[int(fraction*float64(width-1)+.5)] = struct{}{}
	}
	lines := []string{"      " + colorize("┌"+strings.Repeat("─", width)+"┐", 240, color)}
	for row := 0; row < height; row++ {
		label := ticks[row]
		if label == "" {
			label = "     "
		}
		cells := make([]string, width)
		for column := 0; column < width; column++ {
			if mask := masks[row][column]; mask != 0 {
				// Braille characters have one color; cycle overlapping series.
				contributors := make([]int, 0, len(owners[row][column]))
				for candidate := range owners[row][column] {
					contributors = append(contributors, candidate)
				}
				sort.Ints(contributors)
				owner := contributors[column%len(contributors)]
				cells[column] = colorize(string(rune(0x2800)+rune(mask)), colors[owner], color)
				continue
			}
			grid := " "
			if _, ok := ticks[row]; ok {
				grid = "┈"
			} else if _, ok := verticals[column]; ok {
				grid = "┊"
			}
			cells[column] = colorize(grid, 236, color)
		}
		lines = append(lines, " "+colorize(label, 245, color)+colorize("│", 240, color)+strings.Join(cells, "")+colorize("│", 240, color))
	}
	lines = append(lines, "      "+colorize("└"+strings.Repeat("─", width)+"┘", 240, color))
	axis := []rune(strings.Repeat(" ", width))
	fractions := []float64{0, .5, 1}
	if width < 35 {
		fractions = []float64{0, 1}
	}
	for _, fraction := range fractions {
		label := start.Add(time.Duration(fraction * float64(HistorySeconds) * float64(time.Second))).Local().Format("15:04:05")
		offset := int(fraction*float64(width-1)+.5) - len(label)/2
		if offset < 0 {
			offset = 0
		}
		if offset+len(label) > width {
			offset = width - len(label)
		}
		for index, character := range label {
			axis[offset+index] = character
		}
	}
	lines = append(lines, "       "+colorize(string(axis), 245, color))
	return lines
}

func isTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func colorEnabled(w io.Writer) bool {
	return isTerminal(w) && os.Getenv("TERM") != "dumb" && os.Getenv("NO_COLOR") == ""
}

func displayDuration(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	days, rest := seconds/(24*60*60), seconds%(24*60*60)
	hours, rest := rest/(60*60), rest%(60*60)
	minutes := rest / 60
	parts := make([]string, 0, 3)
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 || days > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	parts = append(parts, fmt.Sprintf("%dm", minutes))
	return strings.Join(parts, " ")
}

func displayPercent(value *float64) string {
	if value == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%g%%", *value)
}

func displayReset(reset *Reset) string {
	if reset == nil {
		return ""
	}
	return fmt.Sprintf(" (resets in %s, %s)", displayDuration(reset.AfterSeconds), reset.At)
}

func printWindow(w io.Writer, label string, value *float64, reset *Reset, indent string) {
	fmt.Fprintf(w, "%s%-12s %s%s\n", indent, label+" left:", displayPercent(value), displayReset(reset))
}

func PrintSnapshot(w io.Writer, snapshot Snapshot, settings ...displayOptions) {
	if len(snapshot.Accounts) > 0 {
		for index, account := range snapshot.Accounts {
			if index > 0 {
				fmt.Fprintln(w)
			}
			fmt.Fprint(w, colorize(claudeBucketName(account.Provider)+":\n", providerColor(account.Provider), colorEnabled(w)))
			if account.Snapshot != nil {
				PrintSnapshot(w, *account.Snapshot, settings...)
			} else {
				fmt.Fprint(w, colorize("Unavailable: "+account.Error+"\n", providerColor(account.Provider), colorEnabled(w)))
			}
		}
		return
	}
	opts := displayOptions{}
	if len(settings) > 0 {
		opts = settings[0]
	}
	var output strings.Builder
	printSnapshot(&output, snapshot, opts)
	fmt.Fprint(w, colorize(output.String(), providerColor(snapshot.Provider), colorEnabled(w)))
}

func printSnapshot(w io.Writer, snapshot Snapshot, opts displayOptions) {
	if len(snapshot.Windows) == 0 && snapshot.FiveHourRemainingPercent == nil && snapshot.WeeklyRemainingPercent == nil && len(snapshot.AdditionalRateLimits) == 0 && snapshot.BankedResets == nil && snapshot.ExtraUsage == nil {
		fmt.Fprintln(w, "No quota windows reported. The account may be unavailable or still loading.")
		return
	}
	if snapshot.FiveHourRemainingPercent != nil || snapshot.FiveHourReset != nil {
		printWindow(w, "5h", snapshot.FiveHourRemainingPercent, snapshot.FiveHourReset, "")
	}
	if snapshot.WeeklyRemainingPercent != nil || snapshot.WeeklyReset != nil {
		printWindow(w, "Weekly", snapshot.WeeklyRemainingPercent, snapshot.WeeklyReset, "")
	}
	for _, window := range snapshot.Windows {
		if !opts.visible(window.Name, snapshot.Provider) {
			continue
		}
		if window.Name == "5h" || window.Name == "Weekly" || strings.HasSuffix(window.Name, " / 5h") || strings.HasSuffix(window.Name, " / Weekly") {
			continue
		}
		printWindow(w, window.Name, window.RemainingPercent, window.Reset, "")
	}
	if extra := snapshot.ExtraUsage; extra != nil {
		if !extra.IsEnabled {
			fmt.Fprintln(w, "Extra usage: disabled")
		} else if extra.RemainingPercent == nil {
			fmt.Fprintln(w, "Extra usage: enabled; monthly allowance unavailable")
		}
	}
	if banked := snapshot.BankedResets; banked != nil {
		if banked.Available != nil {
			fmt.Fprintf(w, "Banked resets: %d", *banked.Available)
		} else {
			fmt.Fprint(w, "Banked resets: unavailable")
		}
		if banked.Applicable != nil {
			fmt.Fprintf(w, " (%d applicable now)", *banked.Applicable)
		}
		fmt.Fprintln(w)
		for index, expiration := range banked.Expirations {
			fmt.Fprintf(w, "  %d. expires in %s, %s\n", index+1, displayDuration(expiration.AfterSeconds), expiration.At)
		}
		if banked.ExpirationDetailsPartial {
			fmt.Fprintln(w, "  Some reset expiration details were unavailable.")
		}
	}
	for _, extra := range snapshot.AdditionalRateLimits {
		if !opts.visible(extra.Name, snapshot.Provider) {
			continue
		}
		fmt.Fprintf(w, "\n%s:\n", extra.Name)
		if extra.FiveHourRemainingPercent != nil || extra.FiveHourReset != nil {
			printWindow(w, "5h", extra.FiveHourRemainingPercent, extra.FiveHourReset, "  ")
		}
		if extra.WeeklyRemainingPercent != nil || extra.WeeklyReset != nil {
			printWindow(w, "Weekly", extra.WeeklyRemainingPercent, extra.WeeklyReset, "  ")
		}
	}
}

func snapshotValues(snapshot Snapshot) map[string]float64 {
	values := make(map[string]float64)
	for _, window := range snapshot.Windows {
		if window.RemainingPercent != nil {
			values[window.Name] = clampFloat(*window.RemainingPercent, 0, 100)
		}
	}
	if len(snapshot.Accounts) > 0 {
		return values
	}
	if snapshot.FiveHourRemainingPercent != nil {
		values["5h"] = clampFloat(*snapshot.FiveHourRemainingPercent, 0, 100)
	}
	if snapshot.WeeklyRemainingPercent != nil {
		values["Weekly"] = clampFloat(*snapshot.WeeklyRemainingPercent, 0, 100)
	}
	return values
}

func clampFloat(value, low, high float64) float64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func trimHistory(history []Sample, now time.Time) []Sample {
	cutoff := now.Add(-time.Duration(HistorySeconds) * time.Second)
	first := 0
	for first < len(history) && history[first].At.Before(cutoff) {
		first++
	}
	return history[first:]
}

func terminalSize(w io.Writer) (int, int) {
	if columns, rows, ok := terminalSizeForWriter(w); ok {
		return columns, rows
	}
	if columns, rows, ok := envTerminalSize(); ok {
		return columns, rows
	}
	return 80, 24
}

func envTerminalSize() (int, int, bool) {
	// Terminal dimensions are intentionally conservative without an external
	// dependency. Most PTYs are 80x24, and frame clipping keeps small terminals
	// readable. The chart remains adaptive when COLUMNS/LINES are supplied.
	columns, rows := 0, 0
	if _, err := fmt.Sscanf(os.Getenv("COLUMNS"), "%d", &columns); err != nil {
		columns = 0
	}
	if _, err := fmt.Sscanf(os.Getenv("LINES"), "%d", &rows); err != nil {
		rows = 0
	}
	return columns, rows, columns >= 16 && rows >= 8
}

func clipLine(line string, columns int) string {
	if columns <= 1 {
		return ""
	}
	limit := columns - 1
	visible := 0
	var result strings.Builder
	truncated := false
	for index := 0; index < len(line); {
		if line[index] == '\x1b' && index+1 < len(line) && line[index+1] == '[' {
			end := index + 2
			for end < len(line) && (line[end] < '@' || line[end] > '~') {
				end++
			}
			if end < len(line) {
				end++
			}
			result.WriteString(line[index:end])
			index = end
			continue
		}
		size := 1
		for index+size < len(line) && line[index+size]&0xc0 == 0x80 {
			size++
		}
		if visible >= limit {
			truncated = true
			break
		}
		result.WriteString(line[index : index+size])
		visible++
		index += size
	}
	if truncated && strings.Contains(line, "\x1b[") {
		result.WriteString("\x1b[0m")
	}
	return result.String()
}

func dashboard(w, terminal io.Writer, history []Sample, snapshot *Snapshot, interval float64, lastSuccess time.Time, errorMessage string, color bool, end time.Time, settings ...displayOptions) {
	opts := displayOptions{}
	if len(settings) > 0 {
		opts = settings[0]
	}
	provider := ""
	values := map[string]float64{}
	if snapshot != nil {
		provider = snapshot.Provider
		values = snapshotValues(*snapshot)
	}
	columns, rows := terminalSize(terminal)
	width := clampInt(columns-9, 8, 240)
	namesSet := make(map[string]struct{})
	for _, sample := range history {
		for name := range sample.Values {
			if opts.visible(name, provider) {
				namesSet[name] = struct{}{}
			}
		}
	}
	for name := range values {
		if opts.visible(name, provider) {
			namesSet[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(namesSet))
	for name := range namesSet {
		names = append(names, name)
	}
	sort.Strings(names)
	updated := "waiting"
	if !lastSuccess.IsZero() {
		updated = lastSuccess.Local().Format("15:04:05")
	}
	heading := "CLAUDEX LIMITS"
	if snapshot != nil {
		if provider == "" || provider == "codex" {
			heading = "CODEX LIMITS"
		} else if provider == "claude" {
			heading = "CLAUDE LIMITS"
		} else if provider == "all" {
			heading = "CODEX + CLAUDE LIMITS"
		}
	}
	colors := seriesColors(names, provider)
	if snapshot != nil && snapshot.PlanType == "demo" {
		heading = "CLAUDEX LIMITS · DEMO (synthetic)"
	}
	if provider == "all" {
		heading = colorize("CODEX", codexColor, color) + " + " + colorize("CLAUDE LIMITS", claudeColor, color)
	}
	header := fmt.Sprintf("  %s   ·   LIVE   ·   %gs refresh   ·   %s", heading, interval, updated)
	if provider != "all" {
		header = colorize(header, providerColor(provider), color)
	}
	fmt.Fprintln(w, clipLine(header, columns))
	fmt.Fprintln(w, clipLine(colorize("  LAST 4 HOURS   ·   0–100% left   ·   newest at right", 245, color), columns))
	if len(history) == 0 {
		fmt.Fprintln(w, clipLine("  Waiting for the first sample...", columns))
	} else {
		height := clampInt(rows-8-len(names), 5, 20)
		for _, line := range chartLines(history, end, width, height, names, color, colors, interval) {
			fmt.Fprintln(w, clipLine(line, columns))
		}
	}
	for index, name := range names {
		value, ok := values[name]
		line := fmt.Sprintf("  ━━  %s  ", name)
		if ok {
			line += fmt.Sprintf("%5.1f%% left", value)
		} else {
			line += "unavailable"
		}
		if snapshot != nil {
			for _, window := range snapshot.Windows {
				if window.Name == name && window.Reset != nil {
					line += "  ·  resets in " + displayDuration(window.Reset.AfterSeconds)
				}
			}
		}
		fmt.Fprintln(w, clipLine(colorize(line, colors[index], color), columns))
	}
	if snapshot != nil {
		for _, account := range snapshot.Accounts {
			if account.Error != "" {
				fmt.Fprintln(w, clipLine(colorize("  "+claudeBucketName(account.Provider)+" unavailable · "+account.Error, providerColor(account.Provider), color), columns))
			}
		}
		if banked := snapshot.BankedResets; banked != nil {
			line := "  Banked resets "
			if banked.Available == nil {
				line += "?"
			} else {
				line += fmt.Sprintf("%d", *banked.Available)
			}
			if len(banked.Expirations) > 0 {
				line += "  ·  next expires in " + displayDuration(banked.Expirations[0].AfterSeconds)
			}
			fmt.Fprintln(w, clipLine(colorize(line, codexColor, color), columns))
		}
	}
	if errorMessage != "" {
		fmt.Fprintln(w, clipLine(colorize("  Refresh failed · "+errorMessage+" · retrying; values may be stale", providerColor(provider), color), columns))
	}
	fmt.Fprintln(w, clipLine(colorize("  Ctrl+C quit  ·  last 4h of readings  ·  gaps = missed refreshes", 240, color), columns))
}

func RunLive(w io.Writer, source Source, interval float64, settings ...displayOptions) error {
	interactive := isTerminal(w) && os.Getenv("TERM") != "dumb"
	color := colorEnabled(w)
	if interval <= 0 || !isFinite(interval) || interval > float64(^uint64(0)>>1)/float64(time.Second) || interval*float64(time.Second) < 1 {
		return fmt.Errorf("live interval must be a positive finite number")
	}
	interrupts := make(chan os.Signal, 2)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupts)
	history := make([]Sample, 0)
	if saved, ok := source.(interface{ History(time.Time) []Sample }); ok {
		history = saved.History(time.Now())
	}
	if interactive {
		if _, err := io.WriteString(w, "\x1b[?1049h\x1b[?25l"); err != nil {
			return err
		}
	}
	defer func() {
		if interactive {
			_, _ = io.WriteString(w, "\x1b[?25h\x1b[?1049l")
		}
	}()
	var snapshot *Snapshot
	if provider, ok := source.(interface{ Provider() string }); ok && provider.Provider() != "" {
		snapshot = &Snapshot{Provider: provider.Provider()}
	}
	var lastSuccess time.Time
	render := func(errorMessage string, now time.Time) error {
		var frame strings.Builder
		dashboard(&frame, w, history, snapshot, interval, lastSuccess, errorMessage, color, now, settings...)
		contents := frame.String()
		if interactive {
			contents = "\x1b[H" + strings.ReplaceAll(contents, "\n", "\x1b[K\n") + "\x1b[J"
		}
		_, err := io.WriteString(w, contents)
		return err
	}
	if len(history) > 0 {
		if err := render("", time.Now()); err != nil {
			return err
		}
	}
	for {
		started := time.Now()
		resultCh := make(chan struct {
			snapshot Snapshot
			err      error
		}, 1)
		go func() {
			current, err := source.Read()
			resultCh <- struct {
				snapshot Snapshot
				err      error
			}{snapshot: current, err: err}
		}()
		var current Snapshot
		var err error
		select {
		case result := <-resultCh:
			current, err = result.snapshot, result.err
		case <-interrupts:
			_ = source.Close()
			return nil
		}
		now := time.Now()
		errorMessage := ""
		if err != nil {
			errorMessage = sanitizeError(err.Error())
			history = append(history, Sample{At: now, Values: nil})
		} else {
			copy := current
			snapshot = &copy
			lastSuccess = now
			if current.PlanType == "demo" && len(history) == 0 {
				base := snapshotValues(current)
				for index := 12; index >= 1; index-- {
					values := make(map[string]float64, len(base))
					for name, value := range base {
						variation := float64((index%5)-2) * 2
						values[name] = clampFloat(value+variation, 0, 100)
					}
					history = append(history, Sample{At: now.Add(-time.Duration(index) * 20 * time.Minute), Values: values, Synthetic: true})
				}
			}
			history = append(history, Sample{At: now, Values: snapshotValues(current)})
		}
		history = trimHistory(history, now)
		if err := render(errorMessage, now); err != nil {
			return err
		}
		wait := time.Duration(interval * float64(time.Second))
		if elapsed := time.Since(started); elapsed < wait {
			wait -= elapsed
		} else {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-interrupts:
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
	}
}

func sanitizeError(message string) string {
	message = strings.ReplaceAll(message, "\n", " ")
	message = strings.ReplaceAll(message, "\r", " ")
	return message
}
