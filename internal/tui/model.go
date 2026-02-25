package tui

import (
	"fmt"
	"strings"
	"time"

	"folder-tail/internal/tailer"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

type Config struct {
	Root       string
	Absolute   bool
	Include    []string
	Exclude    []string
	ForceRegex bool
	MaxLines   int
}

type displayLine struct {
	Path    string
	Text    string
	Partial bool
}

type linesMsg []tailer.Line

type errMsg error

type tickMsg time.Time

type Model struct {
	viewport     viewport.Model
	lines        []displayLine
	partialIndex map[string]int
	linesCh      <-chan tailer.Line
	errsCh       <-chan error
	fileCountFn  func() int
	root         string
	absolute     bool
	include      []string
	exclude      []string
	forceRegex   bool
	maxLines     int
	paused       bool
	follow       bool
	lastErr      string
	fileCount    int
	width        int
	height       int
	showPrefixes bool
}

func New(cfg Config, linesCh <-chan tailer.Line, errsCh <-chan error, fileCountFn func() int) Model {
	vp := viewport.New(0, 0)
	return Model{
		viewport:     vp,
		lines:        nil,
		partialIndex: make(map[string]int),
		linesCh:      linesCh,
		errsCh:       errsCh,
		fileCountFn:  fileCountFn,
		root:         cfg.Root,
		absolute:     cfg.Absolute,
		include:      cfg.Include,
		exclude:      cfg.Exclude,
		forceRegex:   cfg.ForceRegex,
		maxLines:     cfg.MaxLines,
		follow:       true,
		showPrefixes: false,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.listenLines(), m.listenErrs(), tickCmd())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeViewport()
		m.refreshViewport()
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case linesMsg:
		for _, line := range []tailer.Line(msg) {
			m.applyLine(line)
		}
		m.refreshViewport()
		if m.follow && !m.paused {
			m.viewport.GotoBottom()
		}
		return m, m.listenLines()
	case errMsg:
		err := error(msg)
		if err != nil {
			m.lastErr = err.Error()
		}
		return m, m.listenErrs()
	case tickMsg:
		if m.fileCountFn != nil {
			m.fileCount = m.fileCountFn()
		}
		return m, tickCmd()
	default:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
}

func (m Model) View() string {
	header := m.headerLines()
	content := m.viewport.View()
	return strings.Join(append(header, content), "\n")
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case " ":
		m.paused = !m.paused
		if !m.paused && m.follow {
			m.viewport.GotoBottom()
		}
		return m, nil
	case "f":
		m.follow = !m.follow
		if m.follow {
			m.viewport.GotoBottom()
		}
		return m, nil
	case "c":
		m.lines = nil
		m.partialIndex = make(map[string]int)
		m.viewport.SetContent("")
		return m, nil
	case "p":
		m.showPrefixes = !m.showPrefixes
		m.refreshViewport()
		return m, nil
	case "up", "pgup", "k", "ctrl+u":
		m.follow = false
	case "down", "pgdown", "j", "ctrl+d":
		if m.viewport.AtBottom() {
			m.follow = true
		}
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m *Model) headerLines() []string {
	status := "RUNNING"
	if m.paused {
		status = "PAUSED"
	}
	follow := "FOLLOW"
	if !m.follow {
		follow = "FREE"
	}
	filters := ""
	if len(m.include) > 0 {
		filters += " include=" + strings.Join(m.include, ",")
	}
	if len(m.exclude) > 0 {
		filters += " exclude=" + strings.Join(m.exclude, ",")
	}
	if m.forceRegex {
		filters += " mode=re"
	}
	pathMode := "path=group"
	if m.showPrefixes {
		pathMode = "path=inline"
	}

	line1 := fmt.Sprintf("[%s %s] %s root=%s files=%d lines=%d%s", status, follow, pathMode, m.root, m.fileCount, len(m.lines), filters)
	if m.lastErr != "" {
		line1 += " err=" + m.lastErr
	}
	line2 := "q quit | space pause | f follow | c clear | arrows scroll"
	return []string{line1, line2}
}

func (m *Model) resizeViewport() {
	headerHeight := 2
	if m.height <= headerHeight {
		m.viewport.Height = 0
		m.viewport.Width = m.width
		return
	}
	m.viewport.Width = m.width
	m.viewport.Height = m.height - headerHeight
}

func (m *Model) applyLine(line tailer.Line) {
	if line.Update {
		idx, ok := m.partialIndex[line.Path]
		if ok && idx >= 0 && idx < len(m.lines) {
			m.lines[idx] = displayLine{Path: line.Path, Text: line.Text, Partial: line.Partial}
			if !line.Partial {
				delete(m.partialIndex, line.Path)
			}
			m.trimLines()
			return
		}
	}

	m.appendLine(line)
}

func (m *Model) appendLine(line tailer.Line) {
	m.lines = append(m.lines, displayLine{Path: line.Path, Text: line.Text, Partial: line.Partial})
	if line.Partial {
		m.partialIndex[line.Path] = len(m.lines) - 1
	} else {
		delete(m.partialIndex, line.Path)
	}
	m.trimLines()
}

func (m *Model) trimLines() {
	if m.maxLines <= 0 {
		return
	}
	if len(m.lines) <= m.maxLines {
		return
	}
	removeCount := len(m.lines) - m.maxLines
	m.lines = m.lines[removeCount:]
	for path, idx := range m.partialIndex {
		newIdx := idx - removeCount
		if newIdx < 0 {
			delete(m.partialIndex, path)
			continue
		}
		m.partialIndex[path] = newIdx
	}
}

func (m *Model) refreshViewport() {
	if m.viewport.Height == 0 {
		return
	}
	builder := strings.Builder{}
	first := true
	if m.showPrefixes {
		for _, line := range m.lines {
			content := formatInlineLine(line)
			if content == "" {
				continue
			}
			if !first {
				builder.WriteByte('\n')
			}
			first = false
			builder.WriteString(content)
		}
	} else {
		lastPath := ""
		for _, line := range m.lines {
			if line.Path != "" && line.Path != lastPath {
				if !first {
					builder.WriteByte('\n')
				}
				first = false
				builder.WriteString("[" + line.Path + "]")
				lastPath = line.Path
			}
			content := formatGroupedLine(line)
			if content == "" {
				continue
			}
			if !first {
				builder.WriteByte('\n')
			}
			first = false
			builder.WriteString(content)
		}
	}
	m.viewport.SetContent(builder.String())
}

func formatInlineLine(line displayLine) string {
	text := line.Text
	if line.Partial {
		text += " ..."
	}
	if line.Path == "" {
		return text
	}
	return line.Path + ": " + text
}

func formatGroupedLine(line displayLine) string {
	text := line.Text
	if line.Partial {
		text += " ..."
	}
	return text
}

func (m Model) listenLines() tea.Cmd {
	return func() tea.Msg {
		line, ok := <-m.linesCh
		if !ok {
			return nil
		}
		lines := []tailer.Line{line}
		for {
			select {
			case next, ok := <-m.linesCh:
				if !ok {
					return linesMsg(lines)
				}
				lines = append(lines, next)
			default:
				return linesMsg(lines)
			}
		}
	}
}

func (m Model) listenErrs() tea.Cmd {
	return func() tea.Msg {
		err, ok := <-m.errsCh
		if !ok {
			return nil
		}
		return errMsg(err)
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}
