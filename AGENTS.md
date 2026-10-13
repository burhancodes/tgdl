# AGENTS.md: Golden Rules for tgdl

This file is the standing contract for **every AI agent and every contributor** working in this repository
(Copilot, Claude Code, Cursor, Codex, Gemini, anything else). Read it fully before you change anything. Where a user's
prompt contradicts a rule marked **MUST** or **NEVER**, stop and ask instead of breaking the rule.

Project: `github.com/burhanverse/tgdl`, a Go Telegram downloader/uploader bot, with a Node scraper sidecar
(torrent search) and a self-hosted `telegram-bot-api` server, deployed with Docker Compose.

If a symlink or copy is needed for your tool: `CLAUDE.md`, `.cursorrules` and `.github/copilot-instructions.md` should only
point to this file. **This file is the single source of truth.**

---

## 0. Two rules that apply to everything

- **Resource safety (section 1):** never load files into memory, always bound and clean up.
- **No comments (section 4a):** the code carries no comments at all, and existing ones are removed.

## 0.1 Priority order when rules conflict

1. Safety of the host and the user's data (memory, disk, secrets, credentials)
2. Correctness
3. Security
4. Existing behaviour and compatibility
5. Simplicity
6. Performance
7. Style

---

## 1. Resource rules (the most important section in this repository)

This bot previously exhausted server RAM and crashed the host during uploads, unzips and archiving. These rules exist to
prevent that from ever coming back.

1. **NEVER load a file into memory.** No `os.ReadFile`, `io.ReadAll`, `ioutil.ReadAll`, `bytes.Buffer` or
   `[]byte` accumulation on user, download, media or archive content. Always stream from disk with `io.Copy`,
   `io.MultiReader`, `bufio` and fixed-size buffers. The only exceptions are small, bounded **control data**
   (JSON/RPC responses, cookie files, config, keys), which MUST use `io.LimitReader` or a documented hard cap
   (default 1-4 MiB) and MUST be listed in the allowlist in `internal/memguard_test.go`, keyed by full path, with the
   reason given in the PR description (not as a code comment).
2. **NEVER buffer a multipart upload.** Use the streaming upload path in `internal/tg/botapi.go` (`postMultipart`,
   exact `Content-Length`, no whole-body buffer). Do not use the go-telegram/bot library's built-in upload helpers for
   files: that library buffers the whole body in memory.
3. **Child process output MUST be bounded.** Use `archive.TailWriter` (or an equivalent ring buffer) for stdout/stderr
   of 7z, unrar, ffmpeg, ffprobe, aria2, yt-dlp, gallery-dl, cyberdrop-dl. Never `cmd.Output()` or `CombinedOutput()` on a
   tool that can print a lot. Always drain stdout/stderr, or the child blocks forever.
4. **Heavy work goes through the heavy semaphore** (`TG_MAX_CONCURRENT_HEAVY`): extraction, archiving, volume splitting,
   ffmpeg remux/convert, any new CPU or I/O heavy job. Wrap external tools with `archive.WrapHeavy` (nice/ionice).
   Never acquire the heavy semaphore while already holding it (deadlock), and never hold it across network waits or user prompts.
5. **Every new feature that touches disk MUST respect the disk guards:** `MIN_FREE_DISK_BYTES`,
   `MAX_TOTAL_DOWNLOADS_BYTES`, `MAX_EXTRACT_BYTES`, `MAX_EXTRACT_FILES`. Check free space (`fsutil.FreeDiskSpace`) before
   large writes, delete intermediates as soon as they are consumed, and keep at most one working copy of a file.
   Prefer `os.Rename` and `os.Link` over copying. A copy of a multi-GB file needs a written reason.
6. **Every new temp file/dir needs an owner and a cleanup path:** create it under `DATA_DIR/tmp` or the job's directory,
   `defer` its removal, register it with the orphan sweeper, and make sure the sweeper cannot delete an *active* one.
   Name patterns must match what `cleanupOrphans` expects. Update both together.
7. **Every goroutine needs an exit.** Take a `context.Context`, stop on cancellation, never leak a goroutine or ticker.
   Use `time.NewTimer`/`Stop` rather than `time.After` in loops. Close response bodies and files with `defer`.
8. **No unbounded collections** (maps, slices, channels, caches) keyed by user input or job data. Give every one a size
   cap, a TTL, or an owner that deletes entries.
9. **Docker limits are part of the design:** `mem_limit`, `cpus`, `pids_limit`, `GOMEMLIMIT`, `oom_score_adj` in
   `docker-compose.yml`. Do not raise or remove them to "fix" something. Find the real cause. If a change needs more memory, say
   so in the PR description with numbers.
10. **Prove it:** a change that touches uploads, downloads, archives, extraction, media or the job pipeline MUST include
    evidence that memory stays flat: a test, or the `scripts/loadtest-upload.sh` / `cmd/loadtest` result with peak RSS
    (`docker stats` or pprof). Say what you measured.

---

## 2. Telegram rules

1. Respect the limits: cloud Bot API 50 MB up / 20 MB down; local server 2 GB. Albums are 2-10 items and bounded by
   `TG_ALBUM_MAX_BYTES`. Captions are at most 1024 chars, messages 4096 (HTML formatting counts).
2. **Never retry an upload once the body was fully sent** (`ErrAmbiguousUpload`). It may already have arrived, and a retry
   makes duplicates. Retry only on connect errors, 429 (honour `retry_after`) and clearly failed requests.
3. Always send user-controlled text with HTML escaping (`status.Esc`, `status.Code`). Never build HTML by plain concatenation of user input.
4. Rate limits: all sends go through the existing limiter (`lim.Acquire` / `AcquireUpload`). Edit status cards at most
   once per ~2 s per message. Do not add a message per file in batch flows, edit one card instead.
5. Handle `FLOOD_WAIT`, `message is not modified`, `message to edit not found` and "bot was blocked" without failing the job.
6. `file://` mode (shared Bot API volume) requires files readable by the Bot API container: mode 0644 files, 0755
   dirs, same volume mapping. Anything small (thumbnails) is attached with multipart, never by path. A `file://` failure must
   fall back to streaming once.
7. Do not log or echo tokens, API hashes, cookies, keys or full user-supplied URLs with credentials.

---

## 3. Security rules

1. **Authorization first.** Every command and every callback checks `auth.Authorized`. Owner-only features check
   `auth.IsOwner` on both the command and the callback. There is **no hardcoded user ID, token, key or default owner** anywhere.
2. **Secrets NEVER enter logs, error strings, status cards, test fixtures, docs or commits.** Mask them (`••••` + last 4)
   when displayed. `.env`, `auth/`, `data/`, `*.session`, cookies, keystores and `settings.json` stay out of git and out of Docker images.
3. **Untrusted input everywhere:** URLs, filenames, archive entries, headers, user arguments, scraper/RPC responses,
   subprocess output.
   - Use `netguard` for outbound URLs (SSRF, private networks) unless `ALLOW_PRIVATE_NETWORK_URLS` is explicitly set.
   - Validate paths: no `..`, no absolute paths from archives, no symlink escapes (zip-slip and tar-slip). Use `filepath.Clean` +
     prefix check against the intended root (`fsutil.IsInsideDir`).
   - Sanitize every filename that came from the outside (length, separators, control chars, reserved names, UTF-8).
   - Never pass user text to a shell. Use `exec.Command` with an argument list, never `sh -c`. Reject user arguments that
     start with `-` where a positional argument is expected, and reject flags that load files or run commands
     (`--config`, `--exec`, `-o`/`--output` paths, `--postprocessor`, etc.) unless explicitly allowlisted.
   - Extraction must enforce size, file-count, depth and ratio limits (decompression bombs).
4. HTTP clients always have timeouts (or a context deadline) and body size limits. TLS verification is never disabled.
5. Run as the non-root user in the container. Do not add capabilities, `privileged`, host networking or extra mounts without
   an explicit reason in the PR.
6. Dependencies: add one only when needed, pin versions, prefer the standard library. For each new dependency state why,
   license and maintenance status. Run `govulncheck ./...` and `npm audit --omit=dev` (scraper) before submitting if they are available.
7. Never fetch and execute remote code at runtime (no `curl | sh`, no downloading scripts at startup). Tools must come from the
   image (pinned versions) and be verified (checksum) if downloaded at build time.

---

## 4. Go code rules

1. Go version is the one in `go.mod`. `gofmt -l .` must print nothing. `go vet ./...` must be clean. Prefer the standard library,
   `log/slog` for logging and `errors.Is/As` with `%w` wrapping.
2. **Errors:** never ignore an error silently. Either handle it, return it wrapped with context, or write `_ =` where ignoring is
   safe (best-effort cleanup such as `_ = os.Remove(tmp)`). No comment is needed or allowed there. Do not `panic` outside `main` startup. Do not `log.Fatal` in library code.
3. **Context:** every function that does I/O, runs a process, sleeps or waits takes `ctx context.Context` as its first
   parameter, and honours cancellation. Jobs must be cancellable from the bot at any point and must clean up after cancel.
4. **Concurrency:** name each mutex after what it guards and put it next to the fields it protects; keep lock scopes short; never call out (Telegram,
   disk, network) while holding a lock; run `go test -race` for anything concurrent. Use `sync/atomic` for hot counters.
5. **Package boundaries:** `internal/<pkg>` stays small and one-purpose. Pure logic (`rename`, `fsutil`, `archive`
   planning, `settings` registry) has no Telegram imports and is table-tested. Transport (`tg`, `bot`) has no business logic.
   `jobs` orchestrates. Do not create import cycles with interface shortcuts. Define small interfaces where they are used.
6. **Config:** every environment variable is read in exactly one place (`internal/config`) and registered in the settings
   registry (if the module exists). A new variable also requires a default, validation, an entry in `.env.example`, and a
   line in `docs/configuration.md`. No `os.Getenv` scattered through the code.
7. **Naming and style:** short, clear names, no stuttering (`archive.Archive`), the code explains itself through names and
   structure (see rule 4.11), no commented-out code, no TODOs, no dead code. Keep functions short;
   split anything over about 80 lines or three nesting levels.
8. **No premature abstraction and no feature creep.** Do the thing that was asked. Do not rewrite unrelated code, rename
   things "while you are there", reformat files you did not change, or add options nobody requested. Unrelated improvements go in a
   separate change.
9. **Backward compatibility:** do not change env var names, command names, persisted formats (SQLite schema, JSON files,
   job IDs, callback data) without a migration and a note in the changelog/docs. Callback data must stay under 64 bytes and be
   validated (never trust it).
10. **Sorting:** use `sort.SliceStable`/`slices.SortStableFunc` when order is user-visible. For file order use `fsutil.SortNatural`.

11. **NO COMMENTS. The codebase stays comment-free.** See section 4a, which is a golden rule.

---

## 4a. Comment policy: zero comments, clean code

**NEVER add a comment to this codebase, and remove every existing one.** If code needs a comment to be understood, the
code is wrong: rename, extract a function, add a type, or simplify. Explanations belong in commit messages, PR
descriptions and `docs/`, not in source files.

Applies to all source: Go (`//`, `/* */`, doc comments, package comments, file headers, license banners), JavaScript in
`scraper/`, shell scripts, Dockerfile, `docker-compose.yml`, workflow YAML, SQL and test files.

**Forbidden (all of it):** explanatory comments, doc comments on exported identifiers, section dividers
(`// ---- foo ----`), `TODO`/`FIXME`/`HACK`/`NOTE`, commented-out code, "removed X" or change-history notes,
author or date stamps, ASCII banners, `// nolint` justifications, `// ignore error` remarks, and trailing comments.

**The only allowed exceptions** are lines a tool needs in order to work. Keep them exactly as they are and add no
prose to them:
- Go compiler and tool directives: `//go:build`, `//go:embed`, `//go:generate`, `//go:linkname`, `//nolint:<linter>` with
  no explanation text, and cgo preambles.
- A shebang (`#!/bin/sh`) and `# syntax=docker/dockerfile:1`.
- Machine-read pragmas in other languages (`// @ts-check`, `/* eslint-disable rule */` with no text).
- `.env.example`: its comments are the user's configuration documentation and stay (no other file gets this exception).
  Markdown files in `docs/` and `README.md` are documentation, not code, and are not affected.

**One-time cleanup (do this as its own commit, "chore: remove all code comments"), done safely:**
1. Never strip comments with a regex or `sed`. `//` appears inside strings, URLs (`https://`), regexes and raw strings.
   Use a real parser:
   - Go: write a small throwaway program (outside the repo, or deleted afterwards) that uses `go/parser` with
     `parser.ParseComments`, removes comment groups except the allowed directives, and prints the file with
     `go/printer` or `format.Source`. Delete the comments from `ast.File.Comments` and the `Doc`/`Comment` fields of
     all declarations, then run `gofmt`.
   - JavaScript: use `terser`/`esbuild` in a non-minifying mode or `recast`/`@babel/parser` with comment removal. Not a regex.
   - Shell, Dockerfile, YAML: remove whole-line and trailing comments with a parser-aware tool or by careful manual review,
     keeping the shebang and `# syntax=` lines. Check that `#` inside quoted strings, heredocs, `$#`, `${#var}` and
     URLs is untouched.
2. **Prove nothing but comments changed.** Before and after, `go build ./...` must succeed and the compiled output must match.
   For Go, compare the ASTs with comments ignored (parse each file with `parser.SkipObjectResolution` and no
   `ParseComments`, print both with `go/printer`, and require identical output), or compare `go vet` and `go test -race`
   results plus `go build -trimpath` hashes of the main binary. Do the same for JS (`node --check`, `npm test`) and run
   `docker compose config -q` and `sh -n` on shell scripts.
3. Removing a comment must not leave it behind as an empty line cluster: run `gofmt -w`, and collapse runs of blank
   lines to one.
4. If removing a comment makes code unclear, **improve the code in the same change** (a better name, a small helper, a named
   constant for a magic number, an error message that says what happened) and keep the behaviour identical. Do not
   describe it in a comment.
5. Where a comment documented a non-obvious limit or a hard-won decision (for example the Telegram size limits, the
   ErrAmbiguousUpload rule, why `postMultipart` streams), move that text to `docs/` (for example `docs/design-notes.md`) or into
   the commit message before deleting it, so the knowledge is not lost. This section is also a good place for it.

**Enforcement (add it with the cleanup):**
- A test, `internal/nocomments_test.go`, walks every `.go` file with `go/parser` and `parser.ParseComments` and fails if it finds any
  comment other than the allowed directives, printing `file:line`. Add an equivalent check for `scraper/` (ESLint
  `no-warning-comments` plus a small script) and for shell and Dockerfile in CI.
- The check runs in `go test ./...` and in the CI workflow, so a comment can never be merged by any agent.
- Generated files are excluded only if they are produced by a tool and marked `// Code generated ... DO NOT EDIT.`;
  that header line is the single allowed exception for them, and the file must be regenerated, never hand-edited.

**Allowed ways to communicate instead of comments:** well-named identifiers and types, small functions, named constants,
tests that show the behaviour, clear error messages and log lines, `docs/*.md`, and commit/PR text.

---

## 5. Testing and verification (you are not done until this passes)

Run all of these and show the result in your final message. Never claim "tests pass" without running them. If you cannot run
something (missing toolchain, no network), say so explicitly instead.

```
gofmt -l .
go vet ./...
go build ./...
go test ./... -race -count=1
(cd scraper && npm ci && npm test)      # only if scraper/ changed
docker compose config -q                # only if compose/Dockerfile changed
```

1. **Every bug fix gets a regression test** that fails without the fix. Every new package gets table-driven tests,
   including error and edge cases (empty, huge, unicode, concurrent, cancelled).
2. Tests are deterministic: no real network, no real Telegram, no sleeps for synchronisation (use channels or a fake clock), no
   dependence on installed external tools (skip with `t.Skip` and a clear message if `ffmpeg`/`7z` is missing).
3. Use the existing fakes (`tg.Client` fake, fake downloaders). Do not mock what you can run for real in a temp dir
   (`t.TempDir()`).
4. `internal/memguard_test.go` and `internal/nocomments_test.go` are hard gates. Do not weaken them, widen an allowlist without a reason in the PR, or key one by
   basename. Add the entry with the **full relative path**, and put the justification in the PR text.
5. Do not delete, skip or loosen a failing test to get green. Fix the code, or explain why the test is wrong.
6. For behaviour that only shows up in production (uploads of 1-2 GB, big archives, many jobs) describe the manual check
   the owner should run after deploying.

---

## 6. Process rules for agents

1. **Read before you write.** Open the relevant files and their tests first. Search for existing helpers
   (`fsutil`, `archive`, `status`, `pacing`, `netguard`) before writing new ones. Do not invent APIs. Check that a function or
   flag exists.
2. **Plan, then change.** For anything bigger than a small fix, write a short plan (files, steps, risks) first. Keep the
   change set small and reviewable. One concern per change.
3. **Ask when it matters, decide when it doesn't.** Ask before: deleting features or data, changing schemas, changing
   public behaviour, adding dependencies, or anything irreversible. Otherwise choose the conventional option and state the assumption.
4. **Never fabricate.** Do not invent results, benchmarks, versions, URLs, flags or file contents. If you did not verify it,
   say "not verified". Report failures honestly, including your own mistakes and anything you left incomplete.
5. **Do not touch** without explicit instruction: `.env`, anything under `auth/`, `data/`, `logs/`, `.git/`, generated files,
   lock files (except through the package manager), CI release settings, and the secrets handling code.
6. **Destructive commands are forbidden unless asked:** `rm -rf` outside a temp dir you created, `git reset --hard`,
   `git push --force`, `docker system prune`, dropping DB tables, deleting volumes.
7. **Docs travel with code.** A change in behaviour, commands, env vars or limits updates `README.md`, `docs/*.md`,
   `.env.example` and the bot's `/help` text in the same change.
8. **Commits:** small, focused, imperative subject (`archive: cap tool output with TailWriter`), body explains why. No
   unrelated reformatting in the same commit.
9. **Finish with a report:** what changed (files), why, how it was verified (the commands above and their output),
   what is not done or is risky, and what the owner must do manually (env vars, migrations, redeploy steps).

---

## 7. Product behaviour that must not regress

- Status card per job, pinned when `PIN_STATUS_MESSAGE` is on, unpinned when the job reaches a final state (not on
  shutdown interrupt, so the job can resume).
- Uploads keep thumbnails/covers for videos and documents, correct kind (video/audio/photo/document), captions, and
  natural file ordering.
- Jobs are cancellable at every stage, resume after a restart, and always clean their directories.
- Archive handling: password prompts, nested archives, multi-volume sets, and the "extract / archive only / both"
  choice. Choosing "both" MUST still upload the original archive.
- One bad file never fails a whole job; it is reported in the skipped list with a reason.
- Mirrors (Pixeldrain, GoFile, etc.) run in the background and never delay or break the Telegram upload.
- The bot works on small hosts (4-6 GB RAM) when the documented limits are used.

Removed features stay removed: the APK patcher, per-user downloader config files (gallery-dl.conf / cyberdrop-dl.yaml) and the
XenForo module must not be re-added unless the owner asks.

---

## 8. Definition of done (checklist)

- [ ] I read the relevant code and this file.
- [ ] The change is minimal, focused and does what was asked, nothing more.
- [ ] No file content is read fully into memory; child output is bounded; heavy work is gated.
- [ ] Temp files, goroutines and processes are cleaned up on success, failure and cancel.
- [ ] I added no comments, and I left none in files I touched (section 4a).
- [ ] Inputs are validated; no secrets in logs; authorization is checked.
- [ ] `gofmt`, `go vet`, `go build`, `go test -race` pass; new tests added.
- [ ] Docs, `.env.example` and `/help` are updated.
- [ ] My final report is honest about what I did not verify.
