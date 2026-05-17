# ft

[![Paid_Go_CLI_TUI_Review](https://img.shields.io/badge/Paid_Go_CLI_TUI_Review-brightgreen)](https://x2.brucelu.top/gotui/checkout/?source=github-badge-folder-tail) [![Ask_First](https://img.shields.io/badge/Ask_First-blue)](https://x2.brucelu.top/products/contact/?offer=gotui&source=github-badge-folder-tail) [![Sample](https://img.shields.io/badge/Sample-informational)](https://x2.brucelu.top/gotui/sample/)

## Paid Go CLI/TUI review

Using this repo as a starting point for a real Go terminal tool, recursive file watcher, log tailer, or long-running TUI? I offer a focused Go CLI/TUI Reliability Review:

- Ask a pre-sales question: https://x2.brucelu.top/products/contact/?offer=gotui&source=github-folder-tail-top
- Sample deliverable: https://x2.brucelu.top/gotui/sample/
- Checkout: https://x2.brucelu.top/gotui/checkout/?source=github-folder-tail-top

Boundary: paid support is engineering review and setup guidance. It does not include managed operation, guaranteed marketplace adoption, or full feature delivery.


TUI tool that tails **all text files** under a folder tree recursively and stays aware of new files as they appear.

## Features
- Recursive tailing for existing files and newly created files.
- Text-only detection to avoid binary noise.
- TUI with scrolling, follow mode, pause/resume, and clear.
- Optional include/exclude glob filters.

## Build

```bash
cd /Users/zongbaolu/work/folder_tail

go build ./cmd/ft
```

## Usage

```bash
./ft [root] [pattern ...]
```

`root` must be an existing directory (default is the current working directory). If the first argument is a directory, it is treated as the root; remaining args are patterns.

## Development

```bash
make test
make build
make run
```

### Flags
- `-n` number of last lines to show on startup per file (default `10`, `0` = start at end)
- `-from-start` show full contents for existing files from the beginning
- `-scan-interval` periodic rescan interval (default `5s`, `0` disables)
- `-absolute` show absolute paths
- `-include` include glob list (comma-separated; matches file name or relative path)
- `-exclude` exclude glob list (comma-separated; matches file name or relative path)
- `-buffer` maximum number of lines kept in the TUI buffer (default `10000`)
- `-max-line-bytes` maximum bytes per line before truncation (default `1048576`)
- `-version` print version and exit
- `-re` / `-regex` treat patterns as regular expressions
- `-r` / `-R` recursive (default true; set `-r=false` to disable)

## Patterns
- Default behavior is recursive; `ft ./*.log` is equivalent to `ft -r ./*.log`.
- Patterns are globs by default. Patterns with `/` (or OS separators) match the **relative path**; otherwise they match the file name.
- Leading `./` or `.\\` is stripped for glob patterns (so `./*.log` behaves like `*.log`).
- To avoid shell expansion, wrap patterns in quotes (for example: `ft '.' '*.log'`).
- Regex mode: use `-re` / `-regex` or prefix a pattern with `re:` (for example: `ft -re '.*\\.log$'` or `ft 're:.*\\.log$'`).

## Examples
```bash
ft .
ft ./*.log
ft /var/log '*.log'
ft /var/log -exclude '*.gz'
ft -re /var/log '.*(err|warn).*\\.log$'
```

## Compatibility
- `folder-tail` is an alias binary that runs the same CLI as `ft`.

### Key bindings
- `q` / `ctrl+c` quit
- `space` pause/resume (still collects new lines)
- `f` toggle follow mode (Follow auto-jumps to newest lines; Free keeps your scroll position)
- `c` clear buffer
- `p` toggle path display (grouped header vs inline)
- arrows / page up/down / `[` `]` scroll

## Notes
- Lines are shown as `path: line`.
- If a line is still being written (no trailing newline), it is shown with `...` and updated when completed.
- Periodic rescans also pull in missed writes if filesystem events were dropped.
- Periodic rescans remove deleted files/directories from the watch set if events were missed.
- Text detection accepts UTF-8 and other non-binary encodings without NUL bytes and rejects common binary signatures/content types.
- When no patterns are provided, common binary extensions (e.g., .wav, .bin, .aiff) are skipped.