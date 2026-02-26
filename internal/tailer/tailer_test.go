package tailer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitLines(t *testing.T) {
	lines, partial := splitLines([]byte("a\nb\r\nc"))
	if len(lines) != 2 || lines[0] != "a" || lines[1] != "b" {
		t.Fatalf("unexpected lines: %#v", lines)
	}
	if string(partial) != "c" {
		t.Fatalf("unexpected partial: %q", string(partial))
	}
}

func TestIsTextData(t *testing.T) {
	if !isTextData([]byte("hello world")) {
		t.Fatalf("expected text data")
	}
	if isTextData([]byte{0x00, 0x01, 0x02}) {
		t.Fatalf("expected binary data")
	}
}

func TestIsTextDataNonUTF8(t *testing.T) {
	// "中文" in GBK encoding (non-UTF8), no NUL bytes.
	gbk := []byte{0xD6, 0xD0, 0xCE, 0xC4}
	if !isTextData(gbk) {
		t.Fatalf("expected non-utf8 text data to be accepted")
	}
	// Mostly control bytes should be treated as binary.
	ctrl := []byte{0x01, 0x02, 0x03, 0x04, 0x7F, 0x08, 0x09}
	if isTextData(ctrl) {
		t.Fatalf("expected control-heavy data to be binary")
	}
}

func TestBinaryExtSkippedWithoutPatterns(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "audio.wav")
	bin := filepath.Join(dir, "data.bin")
	if err := os.WriteFile(wav, []byte("RIFF....WAVE"), 0644); err != nil {
		t.Fatalf("write wav: %v", err)
	}
	if err := os.WriteFile(bin, []byte("BINARYDATA"), 0644); err != nil {
		t.Fatalf("write bin: %v", err)
	}

	tailer := newTestTailer(dir, nil, nil, false)
	if ok, err := tailer.isTextFile(wav); err != nil || ok {
		t.Fatalf("expected wav skipped, ok=%v err=%v", ok, err)
	}
	if ok, err := tailer.isTextFile(bin); err != nil || ok {
		t.Fatalf("expected bin skipped, ok=%v err=%v", ok, err)
	}
}

func TestTailLastLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.log")
	content := "one\ntwo\nthree\nfour\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	lines, partial, err := tailLastLines(path, 2)
	if err != nil {
		t.Fatalf("tailLastLines: %v", err)
	}
	if len(partial) != 0 {
		t.Fatalf("expected no partial, got %q", string(partial))
	}
	if len(lines) != 2 || lines[0] != "three" || lines[1] != "four" {
		t.Fatalf("unexpected lines: %#v", lines)
	}

	content = "one\ntwo\nthree"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	lines, partial, err = tailLastLines(path, 2)
	if err != nil {
		t.Fatalf("tailLastLines: %v", err)
	}
	if string(partial) != "three" {
		t.Fatalf("expected partial 'three', got %q", string(partial))
	}
	if len(lines) != 1 || lines[0] != "two" {
		t.Fatalf("unexpected lines: %#v", lines)
	}
}

func TestReadFromOffsetPartialUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "partial.log")
	if err := os.WriteFile(path, []byte("hello"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tailer := &Tailer{
		cfg:   Config{Root: dir, Absolute: true},
		lines: make(chan Line, 10),
	}
	state := &fileState{}

	if err := tailer.readFromOffset(path, state, 0, false); err != nil {
		t.Fatalf("readFromOffset: %v", err)
	}

	first := <-tailer.lines
	if first.Text != "hello" || !first.Partial || first.Update {
		t.Fatalf("unexpected first line: %#v", first)
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open file: %v", err)
	}
	if _, err := file.WriteString(" world\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := tailer.readFromOffset(path, state, state.offset, true); err != nil {
		t.Fatalf("readFromOffset: %v", err)
	}

	update := <-tailer.lines
	if update.Text != "hello world" || update.Partial || !update.Update {
		t.Fatalf("unexpected update line: %#v", update)
	}
}

func TestReadFromOffsetLongLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "longline.log")
	long := strings.Repeat("a", readChunkSize+512)
	if err := os.WriteFile(path, []byte(long+"\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tailer := &Tailer{
		cfg:   Config{Root: dir, Absolute: true},
		lines: make(chan Line, 10),
	}
	state := &fileState{}

	if err := tailer.readFromOffset(path, state, 0, false); err != nil {
		t.Fatalf("readFromOffset: %v", err)
	}

	line := <-tailer.lines
	if line.Text != long || line.Partial {
		t.Fatalf("unexpected long line: len=%d partial=%v", len(line.Text), line.Partial)
	}
}

func TestShouldInclude(t *testing.T) {
	root := t.TempDir()
	tailer := newTestTailer(root, []string{"*.log"}, []string{"skip.log"}, false)

	if !tailer.shouldInclude(filepath.Join(root, "app.log")) {
		t.Fatalf("expected include for app.log")
	}
	if tailer.shouldInclude(filepath.Join(root, "app.txt")) {
		t.Fatalf("expected exclude for app.txt")
	}
	if tailer.shouldInclude(filepath.Join(root, "skip.log")) {
		t.Fatalf("expected exclude for skip.log")
	}

	tailer = newTestTailer(root, []string{"logs/*.log"}, nil, false)
	if !tailer.shouldInclude(filepath.Join(root, "logs", "app.log")) {
		t.Fatalf("expected include for logs/app.log")
	}
	if tailer.shouldInclude(filepath.Join(root, "other", "app.log")) {
		t.Fatalf("expected exclude for other/app.log")
	}

	tailer = newTestTailer(root, []string{filepath.Join("logs", "*.log")}, nil, false)
	if !tailer.shouldInclude(filepath.Join(root, "logs", "app.log")) {
		t.Fatalf("expected include for logs/app.log with os-specific separator")
	}
}

func TestDisplayPath(t *testing.T) {
	root := t.TempDir()
	tailer := newTestTailer(root, nil, nil, false)
	path := filepath.Join(root, "logs", "app.log")
	if got := tailer.displayPath(path); got == path {
		t.Fatalf("expected relative path, got absolute")
	}

	tailer.cfg.Absolute = true
	if got := tailer.displayPath(path); got != path {
		t.Fatalf("expected absolute path, got %q", got)
	}
}

func TestScanPrunesDeletedFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "app.log")
	if err := os.WriteFile(path, []byte("hello\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tailer, err := New(Config{Root: root})
	if err != nil {
		t.Fatalf("new tailer: %v", err)
	}
	defer tailer.watcher.Close()

	if err := tailer.scanAndRegister(); err != nil {
		t.Fatalf("scanAndRegister: %v", err)
	}
	if got := tailer.FileCount(); got != 1 {
		t.Fatalf("expected 1 file, got %d", got)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove file: %v", err)
	}
	if err := tailer.scanAndRegister(); err != nil {
		t.Fatalf("scanAndRegister: %v", err)
	}
	if got := tailer.FileCount(); got != 0 {
		t.Fatalf("expected 0 files, got %d", got)
	}
}

func TestScanPrunesDeletedDirs(t *testing.T) {
	root := t.TempDir()
	subdir := filepath.Join(root, "logs")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(subdir, "app.log")
	if err := os.WriteFile(path, []byte("hello\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tailer, err := New(Config{Root: root})
	if err != nil {
		t.Fatalf("new tailer: %v", err)
	}
	defer tailer.watcher.Close()

	if err := tailer.scanAndRegister(); err != nil {
		t.Fatalf("scanAndRegister: %v", err)
	}
	if _, ok := tailer.watchedDir[subdir]; !ok {
		t.Fatalf("expected watched dir for subdir")
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove file: %v", err)
	}
	if err := os.Remove(subdir); err != nil {
		t.Fatalf("remove dir: %v", err)
	}

	if err := tailer.scanAndRegister(); err != nil {
		t.Fatalf("scanAndRegister: %v", err)
	}
	if _, ok := tailer.watchedDir[subdir]; ok {
		t.Fatalf("expected subdir watch to be pruned")
	}
}

func TestValidateGlob(t *testing.T) {
	if err := validateGlob("*.log"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := validateGlob(""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := validateGlob("logs/*.log"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := validateGlob("["); err == nil {
		t.Fatalf("expected error for invalid glob")
	}
	if err := validateGlob("logs/["); err == nil {
		t.Fatalf("expected error for invalid glob")
	}
}

func TestRegexPatterns(t *testing.T) {
	root := t.TempDir()
	tailer := newTestTailer(root, []string{"re:.*\\.log$"}, nil, false)
	if !tailer.shouldInclude(filepath.Join(root, "app.log")) {
		t.Fatalf("expected include for regex app.log")
	}
	if tailer.shouldInclude(filepath.Join(root, "app.txt")) {
		t.Fatalf("expected exclude for regex app.txt")
	}

	tailer = newTestTailer(root, []string{".*\\.log$"}, nil, true)
	if !tailer.shouldInclude(filepath.Join(root, "deep", "app.log")) {
		t.Fatalf("expected include for forced regex")
	}
}

func TestCompilePatternsInvalid(t *testing.T) {
	if _, err := compilePatterns([]string{"["}, false); err == nil {
		t.Fatalf("expected error for invalid glob")
	}
	if _, err := compilePatterns([]string{"re:(unclosed"}, false); err == nil {
		t.Fatalf("expected error for invalid regex")
	}
	if _, err := compilePatterns([]string{"(unclosed"}, true); err == nil {
		t.Fatalf("expected error for invalid regex (forced)")
	}
}

func TestNormalizeGlobPattern(t *testing.T) {
	root := t.TempDir()
	tailer := newTestTailer(root, []string{"./*.log"}, nil, false)
	if !tailer.shouldInclude(filepath.Join(root, "app.log")) {
		t.Fatalf("expected include for ./ pattern")
	}
}

func TestMaxLineBytesTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "long.log")
	content := strings.Repeat("a", 50) + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tailer := &Tailer{
		cfg:      Config{Root: dir, Absolute: true, MaxLineBytes: 10},
		lines:    make(chan Line, 10),
		includes: nil,
		excludes: nil,
	}
	state := &fileState{}

	if err := tailer.readFromOffset(path, state, 0, false); err != nil {
		t.Fatalf("readFromOffset: %v", err)
	}
	line := <-tailer.lines
	if !strings.Contains(line.Text, "[truncated]") {
		t.Fatalf("expected truncation marker, got %q", line.Text)
	}
	if len(line.Text) <= 10 {
		t.Fatalf("expected truncated text to include suffix, got len=%d", len(line.Text))
	}
	if line.Partial {
		t.Fatalf("expected non-partial truncated line")
	}
}

func newTestTailer(root string, include, exclude []string, forceRegex bool) *Tailer {
	includes, err := compilePatterns(include, forceRegex)
	if err != nil {
		panic(err)
	}
	excludes, err := compilePatterns(exclude, forceRegex)
	if err != nil {
		panic(err)
	}
	return &Tailer{
		cfg:      Config{Root: root, Include: include, Exclude: exclude, ForceRegex: forceRegex, MaxLineBytes: defaultMaxLine},
		includes: includes,
		excludes: excludes,
	}
}

func TestReadNewAfterTruncate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "truncate.log")
	if err := os.WriteFile(path, []byte("one\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tailer := &Tailer{
		cfg:   Config{Root: dir, Absolute: true},
		lines: make(chan Line, 10),
	}
	state := &fileState{offset: 100}

	if err := os.Truncate(path, 0); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := os.WriteFile(path, []byte("new\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if err := tailer.readNew(path, state); err != nil {
		t.Fatalf("readNew: %v", err)
	}

	line := <-tailer.lines
	if line.Text != "new" || line.Partial {
		t.Fatalf("unexpected line: %#v", line)
	}
}

func TestNonRecursiveScan(t *testing.T) {
	root := t.TempDir()
	subdir := filepath.Join(root, "logs")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rootFile := filepath.Join(root, "root.log")
	subFile := filepath.Join(subdir, "sub.log")
	if err := os.WriteFile(rootFile, []byte("root\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.WriteFile(subFile, []byte("sub\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tailer, err := New(Config{Root: root, Recursive: false, RecursiveSet: true})
	if err != nil {
		t.Fatalf("new tailer: %v", err)
	}
	defer tailer.watcher.Close()

	if err := tailer.scanAndRegister(); err != nil {
		t.Fatalf("scanAndRegister: %v", err)
	}
	if got := tailer.FileCount(); got != 1 {
		t.Fatalf("expected 1 file, got %d", got)
	}
	if _, ok := tailer.states[subFile]; ok {
		t.Fatalf("expected subdir file to be excluded in non-recursive mode")
	}
}
