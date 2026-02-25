package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"folder-tail/internal/tailer"
	"folder-tail/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
)

const defaultMaxLines = 10000

func Run(args []string, version string) int {
	fs := flag.NewFlagSet("ft", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		out := fs.Output()
		fmt.Fprintln(out, "Usage: ft [root] [pattern ...]")
		fmt.Fprintln(out, "")
		fs.PrintDefaults()
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, "Patterns are globs by default. Use -re/-regex or re: prefix for regex.")
		fmt.Fprintln(out, "Examples:")
		fmt.Fprintln(out, "  ft ./*.log")
		fmt.Fprintln(out, "  ft /var/log '*.log'")
		fmt.Fprintln(out, "  ft -re /var/log '.*\\\\.log$'")
	}

	var (
		lines        = fs.Int("n", 10, "number of last lines to show on startup per file (0 = start at end)")
		fromStart    = fs.Bool("from-start", false, "start from beginning for existing files")
		scanInterval = fs.Duration("scan-interval", 5*time.Second, "periodic rescan interval (0 disables)")
		absolute     = fs.Bool("absolute", false, "show absolute paths")
		include      = fs.String("include", "", "optional include patterns (comma-separated, glob by default)")
		exclude      = fs.String("exclude", "", "optional exclude patterns (comma-separated, glob by default)")
		maxLines     = fs.Int("buffer", defaultMaxLines, "max lines to keep in the TUI buffer")
		maxLineBytes = fs.Int("max-line-bytes", 1024*1024, "max bytes per line before truncation")
		showVersion  = fs.Bool("version", false, "print version and exit")
		forceRegex   = fs.Bool("re", false, "treat patterns as regular expressions")
		forceRegex2  = fs.Bool("regex", false, "treat patterns as regular expressions")
		recursive    = fs.Bool("r", true, "recursive (default true)")
		recursive2   = fs.Bool("R", true, "recursive (default true)")
	)

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		fs.Usage()
		return 2
	}

	if *showVersion {
		fmt.Fprintln(os.Stdout, version)
		return 0
	}

	isRecursive := *recursive && *recursive2

	root := "."
	patterns := fs.Args()
	if len(patterns) > 0 {
		if info, err := os.Stat(patterns[0]); err == nil && info.IsDir() {
			root = patterns[0]
			patterns = patterns[1:]
		}
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root = absRoot

	info, err := os.Stat(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !info.IsDir() {
		fmt.Fprintln(os.Stderr, "root is not a directory:", root)
		return 1
	}

	includePatterns := append(parseList(*include), patterns...)
	excludePatterns := parseList(*exclude)

	cfg := tailer.Config{
		Root:         root,
		N:            *lines,
		FromStart:    *fromStart,
		ScanInterval: *scanInterval,
		Absolute:     *absolute,
		Include:      includePatterns,
		Exclude:      excludePatterns,
		ForceRegex:   *forceRegex || *forceRegex2,
		Recursive:    isRecursive,
		RecursiveSet: true,
		MaxLineBytes: *maxLineBytes,
	}

	t, err := tailer.New(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := t.Start(ctx.Done()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	model := tui.New(tui.Config{
		Root:       cfg.Root,
		Absolute:   cfg.Absolute,
		Include:    cfg.Include,
		Exclude:    cfg.Exclude,
		ForceRegex: cfg.ForceRegex,
		MaxLines:   *maxLines,
	}, t.Lines(), t.Errors(), t.FileCount)

	program := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	cancel()
	<-t.Done()
	return 0
}

func parseList(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}
