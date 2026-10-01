package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"time"
)

var version = "dev"

type options struct {
	displayOptions
	json        bool
	live        bool
	interval    float64
	source      string
	authFile    string
	demo        bool
	showHelp    bool
	showVer     bool
	sourceSet   bool
	authSet     bool
	intervalSet bool
}

func parseOptions(args []string) (options, error) {
	var opts options
	fs := flag.NewFlagSet("claudex-limits", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.json, "json", false, "print one machine-readable JSON snapshot")
	fs.BoolVar(&opts.live, "live", false, "show a live four-hour chart including saved readings")
	fs.BoolVar(&opts.showNimbusQuill, "show-nimbus-quill", false, "include Claude's Nimbus Quill quota bucket")
	fs.Float64Var(&opts.interval, "interval", 5, "live refresh interval in seconds")
	fs.StringVar(&opts.source, "source", "auto", "account source: auto, codex, proxy, or claude")
	fs.StringVar(&opts.authFile, "auth-file", "", "read OAuth credentials from this file")
	fs.BoolVar(&opts.demo, "demo", false, "show synthetic data without login or network")
	fs.BoolVar(&opts.showHelp, "help", false, "show this help")
	fs.BoolVar(&opts.showHelp, "h", false, "show this help")
	fs.BoolVar(&opts.showVer, "version", false, "show the program version")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "source":
			opts.sourceSet = true
		case "auth-file":
			opts.authSet = true
		}
	})
	if fs.NArg() != 0 {
		return opts, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if opts.json && opts.live {
		return opts, errors.New("--json and --live cannot be used together")
	}
	if opts.interval <= 0 || !isFinite(opts.interval) || opts.interval > float64(math.MaxInt64)/float64(time.Second) || opts.interval*float64(time.Second) < 1 {
		return opts, errors.New("--interval must be a positive finite number")
	}
	intervalSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "interval" {
			intervalSet = true
		}
	})
	opts.intervalSet = intervalSet
	if !opts.live && intervalSet {
		return opts, errors.New("--interval requires --live")
	}
	if opts.source != "auto" && opts.source != "codex" && opts.source != "proxy" && opts.source != "claude" {
		return opts, fmt.Errorf("invalid --source %q, expected auto, codex, proxy, or claude", opts.source)
	}
	if opts.authSet && opts.sourceSet && opts.source != "auto" && opts.source != "claude" {
		return opts, errors.New("--auth-file cannot be combined with --source codex or --source proxy")
	}
	if opts.source == "claude" && !intervalSet {
		opts.interval = 60
	}
	if opts.demo && (opts.authSet || (opts.sourceSet && opts.source != "auto")) {
		return opts, errors.New("--demo cannot be combined with an account source")
	}
	return opts, nil
}

func printHelp(w io.Writer) {
	fmt.Fprintln(w, "claudex-limits prints remaining Codex or Claude account allowances.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  claudex-limits [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --json                 print one JSON snapshot")
	fmt.Fprintln(w, "  --live                 refresh a four-hour chart including saved readings")
	fmt.Fprintln(w, "  --show-nimbus-quill     include Claude's Nimbus Quill quota bucket")
	fmt.Fprintln(w, "  --interval SECONDS     live refresh interval (default 5; Claude 60)")
	fmt.Fprintln(w, "  --source auto|codex|proxy|claude")
	fmt.Fprintln(w, "  --auth-file PATH       use this OAuth file")
	fmt.Fprintln(w, "  --demo                 use synthetic data, with no login or network")
	fmt.Fprintln(w, "  --version              print the version")
}

func run() int {
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "claudex-limits: %v\n", err)
		fmt.Fprintln(os.Stderr, "Try --help for usage.")
		return 2
	}
	if opts.showHelp {
		printHelp(os.Stdout)
		return 0
	}
	if opts.showVer {
		fmt.Fprintln(os.Stdout, version)
		return 0
	}
	var source Source
	if opts.demo {
		source = NewDemoSource()
	} else {
		source, err = OpenSource(opts.source, opts.authFile, opts.authSet)
		if err != nil {
			if errors.Is(err, errInterrupted) {
				return 0
			}
			fmt.Fprintf(os.Stderr, "claudex-limits: %v\n", err)
			return 1
		}
	}
	if provider, ok := source.(interface{ Provider() string }); ok && provider.Provider() == "claude" && !opts.intervalSet {
		opts.interval = 60
	}
	source = withHistory(source, os.Stderr)
	defer source.Close()
	if opts.live {
		if err := RunLive(os.Stdout, source, opts.interval, opts.displayOptions); err != nil {
			fmt.Fprintf(os.Stderr, "claudex-limits: %v\n", err)
			return 1
		}
		return 0
	}
	snapshot, err := source.Read()
	if err != nil {
		fmt.Fprintf(os.Stderr, "claudex-limits: %v\n", err)
		return 1
	}
	if opts.json {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(snapshot); err != nil {
			fmt.Fprintf(os.Stderr, "claudex-limits: %v\n", err)
			return 1
		}
		return 0
	}
	PrintSnapshot(os.Stdout, snapshot, opts.displayOptions)
	return 0
}

func main() {
	os.Exit(run())
}
