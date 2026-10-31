# Cloud Storage & Google Drive Integration

This guide covers Google Drive downloading (`/gd2tg`), Service Account setup, and direct cloud host uploaders (Pixeldrain, GoFile, FileDitch).

---

## Google Drive Downloader (`/gd2tg`)

Download files or full folder structures from Google Drive directly to Telegram.

### Syntax
```text
/gd2tg <gdrive_url>
```

### Examples
```text
/gd2tg https://drive.google.com/drive/folders/1abc123xyz...
/gd2tg https://drive.google.com/file/d/1abc123xyz.../view
```

---

## Google Drive Authentication Setup

To use `/gd2tg`, credential files must be placed under `auth/<user_id>/` (per-user isolation) or `auth/` (global fallback).

### Method 1: Service Accounts (Recommended)
Service Accounts bypass Google Drive's 750 GB/day per-account download quota and require zero browser steps:
1. Create a Service Account in Google Cloud Console (**IAM & Admin > Service Accounts**).
2. Create and download a **JSON** key.
3. Upload the `.json` key to the Telegram bot chat and **reply to it with `/gd2tg`**. The bot saves it to `auth/<user_id>/accounts/`.

### Method 2: OAuth User Token
1. Generate OAuth credentials JSON file using Google API OAuth credentials.
2. Upload `token.json` (or `credentials.json`) to the Telegram bot chat and **reply to it with `/gd2tg`**. The bot saves it securely with restrictive `0o600` permissions to `auth/<user_id>/token.json`.

### Method 3: Global Credentials (Bot Owner Setup)
If the bot owner places `token.json` or `accounts/*.json` files in the server `auth/` folder, all users can use `/gd2tg` immediately without uploading credentials.

---

## MEGA User Authentication (`/mega -login`)

Per-user MEGA account login allows downloading protected or private content using your personal account. Credentials are isolated under `auth/<user_id>/mega.json` with `0o600` restrictive file permissions.

### Commands & Options
- **Log into account**:
  ```text
  /mega -login email@domain.com:your_password
  /mega -login email@domain.com your_password
  ```
- **Remove saved account**:
  ```text
  /mega -logout
  ```
- **View login status**:
  ```text
  /mega -account
  ```

---

## External Cloud Host Uploaders & Personal API Keys

Upload Telegram media files directly to third-party file hosting services using your own account quota or anonymously.

### Personal API Key Commands
- **GoFile API Key**:
  ```text
  /gofilekey <your_api_token>     # Set your GoFile API token
  /gofilekey                       # Check current key status
  /gofilekey delete                # Remove saved key
  ```
- **Pixeldrain API Key**:
  ```text
  /pdkey <your_api_key>            # Set your Pixeldrain API key
  /pdkey                           # Check current key status
  /pdkey delete                    # Remove saved key
  ```
- **FileDitch Login Token**:
  ```text
  /fdkey <your_login_token>        # Set your FileDitch login token (API key)
  /fdkey                           # Check current key status
  /fdkey delete                    # Remove saved key
  ```

> _Note: If no personal API key is set, uploads default to anonymous mode. Set `ALLOW_SHARED_UPLOAD_KEYS=true` in `.env` if you want users to fall back to the bot owner's global API keys._

### Commands & Limits

| Host | Command | Aliases | Limit | Usage |
| :--- | :--- | :--- | :--- | :--- |
| **GoFile** | `/gfup` | `/gofile`, `/gf` | Unlimited | Reply to a Telegram file or mirror URLs. |
| **FileDitch** | `/fdup` | `/fileditch`, `/fd` | 150 GB (250 GB with account) | Reply to a Telegram file or mirror URLs. |
| **Pixeldrain** | `/pdup` | `/pixeldrain`, `/pd` | 10 GiB | Reply to a Telegram file or mirror URLs. |
