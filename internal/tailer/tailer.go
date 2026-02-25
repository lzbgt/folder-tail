package tailer

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/fsnotify/fsnotify"
)

const (
	defaultSampleSize = 512
	readChunkSize     = 4096
	defaultMaxLine    = 1024 * 1024
)

type patternKind int

const (
	patternGlob patternKind = iota
	patternRegex
)

type pattern struct {
	raw         string
	kind        patternKind
	glob        string
	re          *regexp.Regexp
	pathPattern bool
}

type Config struct {
	Root         string
	N            int
	FromStart    bool
	ScanInterval time.Duration
	Absolute     bool
	Include      []string
	Exclude      []string
	ForceRegex   bool
	Recursive    bool
	RecursiveSet bool
	MaxLineBytes int
}

type Line struct {
	Path    string
	Text    string
	Partial bool
	Update  bool
}

type fileState struct {
	offset           int64
	partial          []byte
	partialDisplayed bool
}

type Tailer struct {
	cfg        Config
	watcher    *fsnotify.Watcher
	lines      chan Line
	errs       chan error
	done       chan struct{}
	states     map[string]*fileState
	watchedDir map[string]struct{}
	includes   []pattern
	excludes   []pattern
	mu         sync.Mutex
}

func New(cfg Config) (*Tailer, error) {
	root := cfg.Root
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	cfg.Root = abs
	if !cfg.RecursiveSet {
		cfg.Recursive = true
	}
	if cfg.MaxLineBytes <= 0 {
		cfg.MaxLineBytes = defaultMaxLine
	}

	includes, err := compilePatterns(cfg.Include, cfg.ForceRegex)
	if err != nil {
		return nil, err
	}
	excludes, err := compilePatterns(cfg.Exclude, cfg.ForceRegex)
	if err != nil {
		return nil, err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	return &Tailer{
		cfg:        cfg,
		watcher:    watcher,
		lines:      make(chan Line, 4096),
		errs:       make(chan error, 64),
		done:       make(chan struct{}),
		states:     make(map[string]*fileState),
		watchedDir: make(map[string]struct{}),
		includes:   includes,
		excludes:   excludes,
	}, nil
}

func (t *Tailer) Lines() <-chan Line {
	return t.lines
}

func (t *Tailer) Errors() <-chan error {
	return t.errs
}

func (t *Tailer) Done() <-chan struct{} {
	return t.done
}

func (t *Tailer) FileCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.states)
}

func (t *Tailer) Start(ctxDone <-chan struct{}) error {
	if !t.cfg.Recursive {
		if err := t.addWatch(t.cfg.Root); err != nil {
			return err
		}
	}
	if err := t.scanAndRegister(); err != nil {
		return err
	}

	go t.loop(ctxDone)
	return nil
}

func (t *Tailer) loop(ctxDone <-chan struct{}) {
	defer func() {
		_ = t.watcher.Close()
		close(t.lines)
		close(t.errs)
		close(t.done)
	}()

	var ticker *time.Ticker
	if t.cfg.ScanInterval > 0 {
		ticker = time.NewTicker(t.cfg.ScanInterval)
		defer ticker.Stop()
	}

	for {
		select {
		case <-ctxDone:
			return
		case event, ok := <-t.watcher.Events:
			if !ok {
				return
			}
			t.handleEvent(event)
		case err, ok := <-t.watcher.Errors:
			if !ok {
				return
			}
			t.sendErr(err)
		case <-t.tickChan(ticker):
			if err := t.scanAndRegister(); err != nil {
				t.sendErr(err)
			}
		}
	}
}

func (t *Tailer) tickChan(ticker *time.Ticker) <-chan time.Time {
	if ticker == nil {
		return nil
	}
	return ticker.C
}

func (t *Tailer) handleEvent(event fsnotify.Event) {
	if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		t.removePath(event.Name)
		return
	}

	if event.Op&fsnotify.Create != 0 {
		t.handleCreate(event.Name)
	}

	if event.Op&fsnotify.Write != 0 {
		t.handleWrite(event.Name)
	}
}

func (t *Tailer) handleCreate(path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	if info.IsDir() {
		if !t.cfg.Recursive {
			return
		}
		if err := t.addWatch(path); err != nil {
			t.sendErr(err)
		}
		_ = t.scanDir(path)
		return
	}
	if info.Mode().IsRegular() {
		t.ensureFile(path)
	}
}

func (t *Tailer) handleWrite(path string) {
	state := t.getState(path)
	if state == nil {
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() {
			t.ensureFile(path)
		}
		return
	}
	if err := t.readNew(path, state); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.removePath(path)
			return
		}
		t.sendErr(err)
	}
}

func (t *Tailer) scanAndRegister() error {
	if !t.cfg.Recursive {
		return t.scanRoot()
	}
	seenFiles := make(map[string]struct{})
	seenDirs := make(map[string]struct{})
	walkErr := filepath.WalkDir(t.cfg.Root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			t.sendErr(err)
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			seenDirs[path] = struct{}{}
			if err := t.addWatch(path); err != nil {
				t.sendErr(err)
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if !t.shouldInclude(path) {
			return nil
		}
		if state := t.getState(path); state != nil {
			if err := t.readNew(path, state); err != nil {
				t.sendErr(err)
			}
			seenFiles[path] = struct{}{}
			return nil
		}
		isText, err := isTextFile(path)
		if err != nil {
			t.sendErr(err)
			return nil
		}
		if isText {
			t.ensureFile(path)
			seenFiles[path] = struct{}{}
		}
		return nil
	})

	t.mu.Lock()
	for path := range t.states {
		if _, ok := seenFiles[path]; !ok {
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				continue
			}
			delete(t.states, path)
		}
	}
	t.mu.Unlock()

	for path := range t.watchedDir {
		if _, ok := seenDirs[path]; !ok {
			if info, err := os.Stat(path); err == nil && info.IsDir() {
				continue
			}
			_ = t.watcher.Remove(path)
			delete(t.watchedDir, path)
		}
	}

	return walkErr
}

func (t *Tailer) scanRoot() error {
	seenFiles := make(map[string]struct{})
	root := t.cfg.Root
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				continue
			}
			continue
		}
		if entry.IsDir() {
			continue
		}
		if !entry.Type().IsRegular() {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if !t.shouldInclude(path) {
			continue
		}
		if state := t.getState(path); state != nil {
			if err := t.readNew(path, state); err != nil {
				t.sendErr(err)
			}
			seenFiles[path] = struct{}{}
			continue
		}
		isText, err := isTextFile(path)
		if err != nil {
			t.sendErr(err)
			continue
		}
		if isText {
			t.ensureFile(path)
			seenFiles[path] = struct{}{}
		}
	}

	t.mu.Lock()
	for path := range t.states {
		if _, ok := seenFiles[path]; !ok {
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				continue
			}
			delete(t.states, path)
		}
	}
	t.mu.Unlock()

	return nil
}

func (t *Tailer) scanDir(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			t.sendErr(err)
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if err := t.addWatch(path); err != nil {
				t.sendErr(err)
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if !t.shouldInclude(path) {
			return nil
		}
		if state := t.getState(path); state != nil {
			if err := t.readNew(path, state); err != nil {
				t.sendErr(err)
			}
			return nil
		}
		isText, err := isTextFile(path)
		if err != nil {
			t.sendErr(err)
			return nil
		}
		if isText {
			t.ensureFile(path)
		}
		return nil
	})
}

func (t *Tailer) addWatch(path string) error {
	if _, ok := t.watchedDir[path]; ok {
		return nil
	}
	if err := t.watcher.Add(path); err != nil {
		return err
	}
	t.watchedDir[path] = struct{}{}
	return nil
}

func (t *Tailer) removePath(path string) {
	if _, ok := t.watchedDir[path]; ok {
		_ = t.watcher.Remove(path)
		delete(t.watchedDir, path)
		prefix := path + string(os.PathSeparator)
		t.mu.Lock()
		for filePath := range t.states {
			if strings.HasPrefix(filePath, prefix) {
				delete(t.states, filePath)
			}
		}
		t.mu.Unlock()
		return
	}

	t.mu.Lock()
	delete(t.states, path)
	t.mu.Unlock()
}

func (t *Tailer) ensureFile(path string) {
	if !t.shouldInclude(path) {
		return
	}
	isText, err := isTextFile(path)
	if err != nil || !isText {
		return
	}

	t.mu.Lock()
	if _, ok := t.states[path]; ok {
		t.mu.Unlock()
		return
	}
	state := &fileState{}
	t.states[path] = state
	t.mu.Unlock()

	if err := t.initFile(path, state); err != nil {
		t.sendErr(err)
	}
}

func (t *Tailer) initFile(path string, state *fileState) error {
	if t.cfg.FromStart {
		return t.readFromOffset(path, state, 0, false)
	}

	if t.cfg.N > 0 {
		lines, partial, err := tailLastLines(path, t.cfg.N)
		if err != nil {
			return err
		}
		for _, line := range lines {
			t.sendLine(Line{Path: t.displayPath(path), Text: line})
		}
		if len(partial) > 0 {
			state.partial = partial
			state.partialDisplayed = true
			t.sendLine(Line{Path: t.displayPath(path), Text: string(partial), Partial: true})
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	state.offset = info.Size()
	return nil
}

func (t *Tailer) readNew(path string, state *fileState) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}

	if info.Size() == state.offset {
		return nil
	}

	if info.Size() < state.offset {
		state.offset = 0
		state.partial = nil
		state.partialDisplayed = false
	}

	return t.readFromOffset(path, state, state.offset, true)
}

func (t *Tailer) readFromOffset(path string, state *fileState, offset int64, includeExistingPartial bool) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return err
	}

	pathDisplay := t.displayPath(path)
	hadPartial := state.partialDisplayed && includeExistingPartial && len(state.partial) > 0
	updatedPartial := false
	var carry []byte
	maxBytes := t.cfg.MaxLineBytes
	if includeExistingPartial && len(state.partial) > 0 {
		carry = append(carry, state.partial...)
	}

	buf := make([]byte, readChunkSize)
	var totalRead int64
	for {
		n, err := file.Read(buf)
		if n > 0 {
			totalRead += int64(n)
			data := buf[:n]
			for len(data) > 0 {
				idx := bytes.IndexByte(data, '\n')
				if idx < 0 {
					carry = append(carry, data...)
					if maxBytes > 0 && len(carry) > maxBytes {
						text, truncated := truncateLineBytes(carry, maxBytes)
						if truncated {
							update := hadPartial && !updatedPartial
							t.sendLine(Line{Path: pathDisplay, Text: text, Update: update})
							if update {
								updatedPartial = true
							}
							carry = carry[:0]
						}
					}
					break
				}
				lineBytes := append(carry, data[:idx]...)
				carry = carry[:0]
				lineBytes = trimTrailingCR(lineBytes)
				text, _ := truncateLineBytes(lineBytes, maxBytes)
				update := hadPartial && !updatedPartial
				t.sendLine(Line{Path: pathDisplay, Text: text, Update: update})
				if update {
					updatedPartial = true
				}
				data = data[idx+1:]
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
	}

	if len(carry) > 0 {
		partial := trimTrailingCR(carry)
		text, truncated := truncateLineBytes(partial, maxBytes)
		update := hadPartial && !updatedPartial
		t.sendLine(Line{Path: pathDisplay, Text: text, Partial: !truncated, Update: update})
		if update {
			updatedPartial = true
		}
		if truncated {
			state.partial = nil
			state.partialDisplayed = false
		} else {
			state.partial = append([]byte(nil), partial...)
			state.partialDisplayed = true
		}
	} else {
		state.partial = nil
		state.partialDisplayed = false
	}

	state.offset = offset + totalRead
	return nil
}

func (t *Tailer) getState(path string) *fileState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.states[path]
}

func (t *Tailer) displayPath(path string) string {
	if t.cfg.Absolute {
		return path
	}
	rel, err := filepath.Rel(t.cfg.Root, path)
	if err != nil {
		return path
	}
	return rel
}

func (t *Tailer) shouldInclude(path string) bool {
	if len(t.includes) == 0 && len(t.excludes) == 0 {
		return true
	}

	name := filepath.Base(path)
	rel, err := filepath.Rel(t.cfg.Root, path)
	if err != nil {
		rel = path
	}

	if len(t.includes) > 0 {
		matched := false
		for _, pattern := range t.includes {
			ok, err := matchCompiledPattern(pattern, name, rel)
			if err != nil {
				continue
			}
			if ok {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	if len(t.excludes) > 0 {
		for _, pattern := range t.excludes {
			ok, err := matchCompiledPattern(pattern, name, rel)
			if err != nil {
				continue
			}
			if ok {
				return false
			}
		}
	}

	return true
}

func (t *Tailer) sendLine(line Line) {
	select {
	case t.lines <- line:
		return
	default:
	}
	select {
	case <-t.lines:
	default:
	}
	select {
	case t.lines <- line:
	default:
	}
}

func (t *Tailer) sendErr(err error) {
	select {
	case t.errs <- err:
	default:
	}
}

func splitLines(data []byte) ([]string, []byte) {
	if len(data) == 0 {
		return nil, nil
	}

	lines := make([]string, 0, 16)
	start := 0
	for i, b := range data {
		if b == '\n' {
			line := data[start:i]
			line = trimTrailingCR(line)
			lines = append(lines, string(line))
			start = i + 1
		}
	}

	if start < len(data) {
		partial := append([]byte(nil), data[start:]...)
		partial = trimTrailingCR(partial)
		return lines, partial
	}

	return lines, nil
}

func tailLastLines(path string, n int) ([]string, []byte, error) {
	if n <= 0 {
		return nil, nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if info.Size() == 0 {
		return nil, nil, nil
	}

	var (
		chunks    [][]byte
		readSize  int64
		remaining = info.Size()
		lineCount int
	)

	for remaining > 0 && lineCount <= n {
		readSize = readChunkSize
		if remaining < readSize {
			readSize = remaining
		}
		remaining -= readSize
		buf := make([]byte, readSize)
		_, err := file.ReadAt(buf, remaining)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, nil, err
		}
		chunks = append(chunks, buf)
		lineCount += bytes.Count(buf, []byte("\n"))
	}

	data := make([]byte, 0, 0)
	for i := len(chunks) - 1; i >= 0; i-- {
		data = append(data, chunks[i]...)
	}

	lines, partial := splitLines(data)
	if len(partial) > 0 {
		if len(lines) > n-1 {
			lines = lines[len(lines)-(n-1):]
		}
		return lines, partial, nil
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil, nil
}

func trimTrailingCR(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	if data[len(data)-1] == '\r' {
		return data[:len(data)-1]
	}
	return data
}

func isTextFile(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()

	buf := make([]byte, defaultSampleSize)
	n, err := file.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return isTextData(buf[:n]), nil
}

func isTextData(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return false
	}
	contentType := http.DetectContentType(data)
	if strings.HasPrefix(contentType, "text/") {
		return true
	}
	switch contentType {
	case "application/json", "application/xml", "application/javascript", "application/x-www-form-urlencoded":
		return true
	default:
		return utf8.Valid(data)
	}
}

func truncateLineBytes(data []byte, max int) (string, bool) {
	if max > 0 && len(data) > max {
		return string(data[:max]) + " [truncated]", true
	}
	return string(data), false
}

func compilePatterns(values []string, forceRegex bool) ([]pattern, error) {
	patterns := make([]pattern, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		kind, raw := patternKindFor(value, forceRegex)
		if kind == patternRegex {
			re, err := regexp.Compile(raw)
			if err != nil {
				return nil, fmt.Errorf("invalid regex pattern %q: %w", value, err)
			}
			patterns = append(patterns, pattern{raw: value, kind: kind, re: re})
			continue
		}

		glob := normalizeGlobPattern(raw)
		pathPattern := usesPathPattern(glob)
		if err := validateGlob(glob); err != nil {
			return nil, fmt.Errorf("invalid glob pattern %q: %w", value, err)
		}
		patterns = append(patterns, pattern{
			raw:         value,
			kind:        kind,
			glob:        glob,
			pathPattern: pathPattern,
		})
	}
	return patterns, nil
}

func patternKindFor(value string, forceRegex bool) (patternKind, string) {
	if forceRegex {
		return patternRegex, stripRegexPrefix(value)
	}
	if strings.HasPrefix(value, "re:") {
		return patternRegex, strings.TrimPrefix(value, "re:")
	}
	return patternGlob, value
}

func stripRegexPrefix(value string) string {
	return strings.TrimPrefix(value, "re:")
}

func normalizeGlobPattern(pattern string) string {
	if strings.HasPrefix(pattern, "./") || strings.HasPrefix(pattern, ".\\") {
		return pattern[2:]
	}
	return pattern
}

func validateGlob(pattern string) error {
	if pattern == "" {
		return nil
	}
	if usesPathPattern(pattern) {
		_, err := path.Match(filepath.ToSlash(pattern), "dir/file")
		return err
	}
	_, err := filepath.Match(pattern, "file")
	return err
}

func matchCompiledPattern(pattern pattern, name, rel string) (bool, error) {
	switch pattern.kind {
	case patternRegex:
		rel = filepath.ToSlash(rel)
		return pattern.re.MatchString(rel), nil
	case patternGlob:
		if pattern.pathPattern {
			rel = filepath.ToSlash(rel)
			return path.Match(pattern.glob, rel)
		}
		return filepath.Match(pattern.glob, name)
	default:
		return false, nil
	}
}

func usesPathPattern(pattern string) bool {
	return strings.ContainsAny(pattern, "/\\")
}
