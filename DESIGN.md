# Folder Tail - Design

## Goals
- Tail all text files under a folder tree recursively.
- Remain aware of newly created files and start tailing them automatically.
- Provide a **TUI** that renders a live, scrollable stream of lines with file path prefixes.
- Keep the implementation robust against truncation/rotation and missed events.

## Non-Goals
- Binary log support.
- Exact parity with GNU coreutils `tail` options.

## Constraints
- Cross-platform (macOS/Linux/Windows) where fsnotify supports filesystem events.
- Prefer standard library; allow small, well-known dependencies for TUI + file watching.

## Architecture
- **Initial scan**: Walk the root directory, detect text files, and tail the last N lines.
- **Watcher**: Use fsnotify to watch all directories recursively. On new directories, add watches. On file create/write/rename, ensure the file is registered and read appended content.
- **Periodic rescan**: Optional scan interval to discover files that might be missed by events.
- Periodic rescan also checks tracked files for new data in case events were dropped.
- **File state**: Track each file with current offset and a partial line buffer to handle writes without trailing newline.
- **Tail reader**:
  - On event or rescan, open file, handle truncation (size < offset), seek to offset, read new bytes, split by `\n`, emit complete lines, and keep incomplete remainder.
  - For initial tailing, read from end in chunks until N lines are found.
- **Text detection**: Use a small sample (first 512 bytes) and treat as text when no NUL bytes are present and content type looks textual.

## TUI
- Use a terminal UI library (Bubble Tea) for rendering and input handling.
- Show a header/status line (root path, filters, total files watched, paused/running).
- Show a scrollable viewport of recent lines; newest at bottom.
- Provide key bindings: pause/resume, follow (jump to bottom), clear, and quit.

## CLI
- `ft [root] [pattern ...]`
- Flags:
  - `-n` (int): number of last lines to show on startup per file (default 10, 0 = start at end).
  - `-from-start` (bool): start from beginning for existing files.
  - `-scan-interval` (duration): periodic rescan interval (default 5s; 0 disables).
  - `-absolute` (bool): show absolute paths.
  - `-include`/`-exclude` (glob): optional file name filters (comma-separated).
  - `-buffer` (int): maximum number of lines kept in the TUI buffer.
  - `-max-line-bytes` (int): maximum bytes per line before truncation.
  - `-re`/`-regex` (bool): treat patterns as regular expressions.
  - `-r`/`-R` (bool): recursive (default true; set `-r=false` to disable).

- Positional args are patterns. If the first arg is a directory, it is treated as the root.
- Patterns are globs by default; use `re:` prefix or `-re` to enable regex.

## Output Format
- Lines are rendered in the TUI as `path: line`.
- Partial lines (no trailing newline yet) are shown with `...` and updated when completed.
- Path defaults to relative to root unless `-absolute` is set.

## Testing
- Unit tests for:
  - Extracting last N lines from a byte slice.
  - Text detection heuristics.
  - Tail reader handling truncation and partial lines.
