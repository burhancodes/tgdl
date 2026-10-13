# File Renaming (`/rename`)

Telegram does not support in-place file renaming. Renaming a file therefore downloads the file, renames it on disk using zero-copy `os.Rename`, and re-uploads it to Telegram. Memory usage remains flat because files are never loaded into memory.

---

## Commands

### Single File Mode
Reply to an existing Telegram file (or upload a file with the caption):
```text
/rename <scheme> [options]
```

Examples:
- `/rename {n:3} - {name}.{ext}`
- `/rename {name|slug}.{ext}`
- `/rename {date:2006-01-02}_{name}.{ext} --doc`

### Batch Mode
Rename a collection of files sequentially:
1. Start a session:
   ```text
   /rename batch <scheme> [options]
   ```
2. Send files (documents, videos, audio, photos, or media albums) to the chat. Files are registered in memory with their Telegram `file_id` without downloading yet.
3. Use the interactive buttons on the batch card:
   - **Preview**: Displays an `old -> new` mapping table for up to 30 files, along with any warnings or conflicts.
   - **Apply**: Enqueues the batch rename job.
   - **Clear**: Resets the current file list.
   - **Cancel**: Terminates the batch session.
4. Additional batch commands:
   - `/rename scheme <new scheme>`: Updates the scheme for the current batch session without losing files.
   - `/rename preset <name>`: Applies a built-in preset to the current batch session.

### Archive Contents Renaming
Reply to an archive with `/rename batch <scheme>`:
- The archive is extracted using the extraction pipeline.
- Extracted files are renamed according to `<scheme>` before upload.

---

## Options

| Flag | Values | Default | Description |
| :--- | :--- | :--- | :--- |
| `--start` | Integer | `1` | Starting counter for `{n}` |
| `--step` | Integer | `1` | Increment step for `{n}` |
| `--sort` | `name`, `time` / `mtime`, `size`, `sent` | `sent` | Sort ordering for batch |
| `--on-conflict` | `suffix`, `skip`, `error` | `suffix` | Collision resolution mode (suffix appends ` (2)`, ` (3)`) |
| `--doc` | Flag | Off | Force upload as document instead of media type |

---

## Scheme Syntax

Template tokens are enclosed in braces `{...}`. Literal braces can be escaped using `{{` and `}}`.

### Tokens

| Token | Example | Description |
| :--- | :--- | :--- |
| `{name}` | `{name}` | Original filename without extension |
| `{ext}` | `{ext}` | Extension without the leading dot |
| `{n}` | `{n}`, `{n:3}` | Sequential counter; optional `:width` specifies zero-padding |
| `{date}` | `{date:2006-01-02}` | Current processing timestamp formatted with Go time layout |
| `{mtime}` | `{mtime:2006-01-02}` | Source file modification time (when known) |
| `{size}` | `{size}`, `{size:bytes}` | File size formatted in human-readable units or raw bytes |
| `{parent}` | `{parent}` | Source directory or archive name |
| `{rand:N}` | `{rand:6}` | Random alphanumeric string of length N |
| `{title}` | `{title}` | Media metadata title tag |
| `{artist}` | `{artist}` | Media metadata artist / performer tag |
| `{track}` | `{track}` | Media metadata track tag |
| `{year}` | `{year}` | Media metadata release year tag |
| `{season}` | `{season}`, `{season:2}` | TV season number extracted from `SxxEyy` filename patterns |
| `{episode}` | `{episode}`, `{episode:2}` | TV episode number extracted from `SxxEyy` filename patterns |

### Token Modifiers

Modifiers are chained after a pipe `|` within a token:

| Modifier | Syntax | Description |
| :--- | :--- | :--- |
| `lower` | `{name\|lower}` | Lowercase string |
| `upper` | `{name\|upper}` | Uppercase string |
| `title` | `{name\|title}` | Title case string |
| `slug` | `{name\|slug}` | URL/filesystem-safe slug (letters, numbers, hyphens) |
| `trim` | `{name\|trim}` | Trims leading and trailing whitespace |
| `replace` | `{name\|replace:old:new}` | Replaces substrings |
| `regex` | `{name\|regex:pat:rep}` | Regular expression replacement (capped at 200 pattern chars) |
| `cut` | `{name\|cut:start:end}` | Substring slice by rune offsets |
| `remove` | `{name\|remove:[chars]}` | Removes all characters in set |

### Presets

| Name | Template Scheme |
| :--- | :--- |
| `sequence` | `{n:3} - {name}.{ext}` |
| `clean` | `{name\|slug}.{ext}` |
| `date-prefix` | `{date:2006-01-02}_{name}.{ext}` |
| `tv` | `{parent} - S{season:2}E{episode:2}.{ext}` |

---

## Safety & Sanitization

1. **Path Safety**: Path separators (`/`, `\`) and control characters are stripped. Relative paths `.` and `..` are forbidden.
2. **Length Guard**: Filenames are strictly capped at 255 bytes, preserving valid UTF-8 character boundaries and never truncating the file extension.
3. **Collision Detection**: Collisions inside a batch or with existing files default to appending ` (2)`, ` (3)` unless `--on-conflict` is set to `skip` or `error`.
4. **Resumption**: Planned file mappings are persisted with the job in the database. Interrupted jobs resume with identical filenames.
