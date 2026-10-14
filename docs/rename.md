# File Renaming (`/rename`)

Rename uses exactly what the user types. No scheme, template, token, preset, option, or auto-detection is interpreted.

The file extension (`.mkv`, `.mp4`, etc.) is kept automatically if omitted.

---

## Examples

### 1. Single File
Reply to a file (or send a file with caption):
```text
/rename The Cold Sato-san S01E01
```
Result: `The Cold Sato-san S01E01.mkv`

If you send `/rename` alone on a file, the bot asks "Send the new name" and uses your next message verbatim.

### 2. Several Files (Batch)
1. Send `/rename batch`.
2. Send the media files to the chat.
3. Send one message with one new name per line:
   ```text
   The Cold Sato-san S01E01
   The Cold Sato-san S01E02
   The Cold Sato-san S01E03
   ```
4. A preview is displayed with `[Apply]` and `[Cancel]`. Click `[Apply]` to start renaming.
