package settings

import (
	"fmt"
	"strings"
)

var orderedGroups = []string{
	GroupGeneral,
	GroupTelegram,
	GroupLimits,
	GroupStorage,
	GroupArchives,
	GroupDownloaders,
	GroupUploadHosts,
	GroupCloud,
	GroupTorrents,
	GroupLogging,
	GroupSecurity,
}

func GenerateDocs(reg *Registry) string {
	var sb strings.Builder

	sb.WriteString("# Bot Settings & Runtime Configuration\n\n")
	sb.WriteString("The bot features a centralized, typed settings registry allowing the bot owner to inspect and modify configuration variables at runtime via Telegram without manually editing `.env`.\n\n")
	sb.WriteString("---\n\n")

	sb.WriteString("## 1. Precedence Rules\n\n")
	sb.WriteString("Configuration values are resolved using four distinct layers in increasing order of priority:\n\n")
	sb.WriteString("1. **Built-in Defaults**: Hardcoded fallback values defined in `internal/settings`.\n")
	sb.WriteString("2. **`.env` File**: Key-value pairs loaded at startup from `.env`.\n")
	sb.WriteString("3. **Process Environment**: Environment variables set in the host OS or Docker container.\n")
	sb.WriteString("4. **Bot Overrides (`DATA_DIR/settings.json`)**: Overrides applied dynamically by the owner via Telegram.\n\n")
	sb.WriteString("Precedence resolution:\n")
	sb.WriteString("```\n")
	sb.WriteString("Built-in Default < .env < Process Environment < settings.json override\n")
	sb.WriteString("```\n\n")
	sb.WriteString("In the bot UI (`/settings` detail cards and `/get`), the active layer is explicitly displayed:\n")
	sb.WriteString("- `default`: using compiled fallback.\n")
	sb.WriteString("- `env`: loaded from environment or `.env`.\n")
	sb.WriteString("- `bot override`: active override stored in `settings.json`.\n\n")
	sb.WriteString("---\n\n")

	sb.WriteString("## 2. Apply Modes\n\n")
	sb.WriteString("Every configuration setting belongs to one of three application categories:\n\n")
	sb.WriteString("- **Live**: Changes take effect immediately without requiring a restart.\n")
	sb.WriteString("  - Concurrency semaphores (`TG_MAX_CONCURRENT_DOWNLOADS`, `TG_MAX_CONCURRENT_UPLOADS`, `TG_MAX_CONCURRENT_HEAVY`) are dynamically resized without dropping in-flight permits.\n")
	sb.WriteString("  - `LOG_LEVEL` shifts `slog.LevelVar` in real time.\n")
	sb.WriteString("  - `GLOBAL_DOWNLOAD_SPEED_LIMIT` applies to aria2 live via JSON-RPC (`aria2.changeGlobalOption`) and tunes new download tasks.\n")
	sb.WriteString("  - Album limits (`TG_ALBUM_MAX_BYTES`), disk space guards (`MIN_FREE_DISK_BYTES`), extraction caps (`MAX_EXTRACT_BYTES`, `MAX_EXTRACT_FILES`), `AUTHORIZED_USER_IDS`, upload host keys, search limits, and rename options are hot-applied.\n")
	sb.WriteString("- **Restart Required**: Overrides are saved immediately to `settings.json`, but require a bot restart to take effect.\n")
	sb.WriteString("  - Indicated in the bot UI with a `[restart required]` badge.\n")
	sb.WriteString("  - The owner can issue `/restart` for a clean shutdown (exit code 0); Docker's `restart: unless-stopped` policy restarts the container.\n")
	sb.WriteString("- **Read-Only**: Critical bootstrapping variables that cannot be altered within the bot:\n")
	sb.WriteString("  - `OWNER_ID`, `TG_BOT_TOKEN`, `TG_API_ID`, `TG_API_HASH`, `TG_BOT_API_URL`, `TG_BOT_API_DATA_DIR`, `TG_BOT_API_LOCAL_DIR_MAP`, `DATA_DIR`, `AUTH_DIR`, `LOG_DIR`, and `GDRIVE_*`.\n")
	sb.WriteString("  - Displayed in the bot for inspection with a `[read-only]` badge, but can only be changed via the host environment or `.env` file.\n\n")
	sb.WriteString("---\n\n")

	sb.WriteString("## 3. Persistence, Backups & Encryption\n\n")
	sb.WriteString("- **File Location**: Overrides are stored in `DATA_DIR/settings.json` with strict `0600` permissions.\n")
	sb.WriteString("- **Atomic Writes**: Saved atomically using a temporary file in the same directory (`settings.json.tmp.*`), synced to disk (`Sync()`), and renamed.\n")
	sb.WriteString("- **Automatic Backup**: A backup of the prior configuration is saved to `DATA_DIR/settings.json.bak` prior to renaming.\n")
	sb.WriteString("- **Optional `.env` Synchronization**: When `SETTINGS_WRITE_ENV=true` is enabled, changes also atomically update matching `KEY=value` lines in `.env` while preserving comments and ordering.\n")
	sb.WriteString("- **Secret Encryption**: When `SETTINGS_ENCRYPTION_KEY` is provided, secret values are encrypted with AES-256-GCM using an scrypt-derived key. If the encryption key is missing or incorrect, loading fails safely rather than falling back to defaults.\n")
	sb.WriteString("- **Resetting Configuration**: To wipe all bot overrides and restore environment defaults, delete `DATA_DIR/settings.json` and restart the bot, or use `/unset KEY` in Telegram.\n\n")
	sb.WriteString("---\n\n")

	sb.WriteString("## 4. Docker Volume Persistence\n\n")
	sb.WriteString("Ensure `DATA_DIR` (`./data:/app/data` in `docker-compose.yml`) is mounted as a persistent host directory or named volume. Overrides in `settings.json` will survive container rebuilds and image updates.\n\n")
	sb.WriteString("---\n\n")

	sb.WriteString("## 5. Owner Commands & UI Reference\n\n")
	sb.WriteString("Settings commands and callbacks are strictly restricted to the bot owner (`OWNER_ID`) in a private chat.\n\n")
	sb.WriteString("### Interactive Menu\n")
	sb.WriteString("- `/settings`: Opens the interactive settings menu categorized by group.\n")
	sb.WriteString("  - Group navigation with 8 keys per page, status badges, and masked secret values (`••••1a2b` or `••••••••`).\n")
	sb.WriteString("  - Detail cards with key type, allowed range, current value, source layer, and apply mode.\n")
	sb.WriteString("  - `[Edit]` button prompts for input with a 60-second timeout; private messages containing secrets are deleted immediately after reading.\n")
	sb.WriteString("  - Boolean and enum values toggle directly via inline buttons.\n")
	sb.WriteString("  - Confirmation dialogs protect dangerous modifications (Security group, `AUTHORIZED_USER_IDS`, `MIN_FREE_DISK_BYTES=0`, secrets).\n")
	sb.WriteString("  - Rate-limited to 30 edits per minute with audit logging.\n\n")
	sb.WriteString("### Text Commands\n")
	sb.WriteString("- `/set KEY value`: Set or override a configuration key.\n")
	sb.WriteString("- `/get KEY`: Inspect a configuration key's value, source layer, and details.\n")
	sb.WriteString("- `/unset KEY`: Remove a configuration override and revert to the default/env value.\n")
	sb.WriteString("- `/settings search <text>`: Search settings by key name or description.\n")
	sb.WriteString("- `/settings changed`: View all currently active configuration overrides.\n")
	sb.WriteString("- `/settings history`: Display audit history of the last 50 configuration changes.\n")
	sb.WriteString("- `/settings undo`: Revert the most recent configuration change.\n")
	sb.WriteString("- `/settings export`: Export configuration as a JSON document (secrets redacted).\n")
	sb.WriteString("- `/settings import`: Import configuration from an uploaded JSON file with diff confirmation.\n")
	sb.WriteString("- `/restart`: Gracefully flush state and shut down with exit code 0 to trigger container restart.\n\n")
	sb.WriteString("---\n\n")

	sb.WriteString("## 6. Complete Settings Reference\n\n")

	for _, groupName := range orderedGroups {
		specs := reg.InGroup(groupName)
		if len(specs) == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("### %s\n\n", groupName))
		sb.WriteString("| Key | Type | Default | Apply Mode | Description |\n")
		sb.WriteString("|---|---|---|---|---|\n")
		for _, s := range specs {
			applyStr := "Live"
			switch s.Apply {
			case ApplyRestart:
				applyStr = "Restart Required"
			case ApplyReadOnly:
				applyStr = "Read-Only"
			}
			defVal := s.Default
			if defVal == "" {
				defVal = "*(empty)*"
			}
			sb.WriteString(fmt.Sprintf("| `%s` | `%s` | `%s` | %s | %s |\n",
				s.Key, s.Type, defVal, applyStr, s.Description))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
