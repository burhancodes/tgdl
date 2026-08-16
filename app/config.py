import os
from pathlib import Path
from typing import Any, Literal

from pydantic import Field, field_validator
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", env_file_encoding="utf-8", extra="ignore")

    # --- Telegram / MTProto credentials ---
    tg_api_id: int = Field(default_factory=lambda: int(os.getenv("TG_API_ID", "0")), description="From https://my.telegram.org")
    tg_api_hash: str = Field(default_factory=lambda: os.getenv("TG_API_HASH", ""), description="API hash from Telegram")
    tg_bot_token: str = Field(default_factory=lambda: os.getenv("TG_BOT_TOKEN", ""), description="Bot token from @BotFather")
    pixeldrain_api_key: str | None = Field(default=None, description="API key for Pixeldrain uploads")
    pixeldrain_domain: str = Field(default="pixeldrain.com", description="Domain to use for Pixeldrain uploads and links (pixeldrain.com or pixeldra.in)")
    gofile_api_key: str | None = Field(default=None, description="API token for GoFile uploads")
    allow_shared_upload_keys: bool = Field(default=False, description="Allow falling back to global bot owner API keys for webhost uploads")
    allow_private_network_urls: bool = Field(default=False, description="Allow downloading URLs resolving to private/reserved IP ranges")
    show_system_stats_on_job_card: bool = Field(default=True, description="Show host CPU/RAM/disk/network/uptime stats on individual job status cards")


    # --- APK Patcher & JKS Keystore settings ---
    keystore_path: Path | None = Field(default=None, description="Optional global JKS keystore file path")
    keystore_pass: str | None = Field(default=None, description="Optional global JKS keystore password")
    key_alias: str | None = Field(default=None, description="Optional global JKS key alias")
    key_pass: str | None = Field(default=None, description="Optional global JKS key password")

    # --- Storage locations ---
    data_dir: Path = Field(default=Path("./data"))
    auth_dir: Path = Field(default=Path("./auth"))

    # --- Google Drive settings ---
    gdrive_token_path: Path = Field(default=Path("./auth/token.json"))
    gdrive_accounts_dir: Path = Field(default=Path("./auth/accounts"))
    use_service_accounts: bool = Field(default=True)

    # --- MEGA settings ---
    mega_email: str | None = Field(default=None, description="Optional MEGA account email")
    mega_password: str | None = Field(default=None, description="Optional MEGA account password")

    # --- gallery-dl config & pacing ---
    gdl_config_path: Path = Field(
        default=Path("./app/downloader/gallery_dl/gallery-dl.conf"),
        description="Path to default gallery-dl configuration file",
    )
    gdl_sleep_min: float = 1.5
    gdl_sleep_max: float = 4.0
    gdl_sleep_request: str = "1-3"
    global_download_speed_limit: str | None = Field(default="20M", description="Global download rate limit for tools supporting it (e.g. 3M, 500K, None for unlimited)")
    hls_download_timeout: int = Field(default=300, description="Timeout for HLS download operations in seconds")
    gdl_retries: int = 4

    # --- cyberdrop-dl config & pacing ---
    cdl_config_path: Path = Field(
        default=Path("./app/downloader/cyberdrop_dl/config.yaml"),
        description="Path to default cyberdrop-dl configuration file",
    )
    cdl_retries: int = 3
    cdl_max_run_retries: int = 3
    cdl_backoff_base_s: float = 30.0
    cdl_backoff_multiplier: float = 2.5

    # --- adaptive backoff on rate-limit signals ---
    gdl_max_run_retries: int = 3
    gdl_backoff_base_s: float = 30.0
    gdl_backoff_multiplier: float = 2.5

    # --- Telegram upload pacing ---
    tg_upload_delay_min: float = 2.0
    tg_upload_delay_max: float = 4.5
    tg_batch_size: int = 30
    tg_batch_cooldown_s: float = 25.0
    tg_upload_max_retries: int = 3
    tg_max_concurrent_downloads: int = 1
    tg_max_concurrent_uploads: int = 1  # keep at 1 unless you know Telegram tolerates more

    # --- Torrent Search settings ---
    torrent_timeout: int = Field(default=120, description="Torrent download and RPC request timeout in seconds")
    search_limit: int = Field(default=300, description="Limit of search results to fetch for Telegraph pagination")
    magnetio_rpc_url: str = Field(default="http://magnetio-scraper:8080/rpc", description="Magnetio JSON-RPC sidecar URL")
    magnetio_rpc_secret: str | None = Field(default=None, description="Optional Magnetio JSON-RPC secret token")

    # --- Authorization / Access Control ---
    owner_id: int | None = Field(default=1623457379, description="Telegram User ID of the bot owner (env OWNER_ID)")
    authorized_user_ids: list[int] | str = Field(default_factory=list, description="List of allowed Telegram user IDs (comma-separated env AUTHORIZED_USER_IDS)")

    # --- Job & Disk limits ---
    max_jobs_per_chat: int = Field(default=3, description="Maximum active+queued jobs per chat")
    max_total_downloads_bytes: int | None = Field(default=None, description="Optional cap on total download folder disk usage in bytes")

    # --- misc ---
    max_upload_bytes: int = 2 * 1024 * 1024 * 1024  # 2GB, MTProto ceiling
    log_level: Literal["DEBUG", "INFO", "WARNING", "ERROR"] = Field(
        default="INFO", description="Log level (DEBUG, INFO, WARNING, ERROR)"
    )
    log_format: str = Field(default="text", description="Log format: 'text' or 'json'")
    log_dir: Path = Field(default=Path("./logs"))

    @field_validator("log_level", mode="before")
    @classmethod
    def _uppercase_log_level(cls, v: Any) -> Any:
        if isinstance(v, str):
            return v.strip().upper()
        return v

    @field_validator("owner_id", mode="before")
    @classmethod
    def _parse_owner_id(cls, v: Any) -> int | None:
        if v == "" or v is None:
            return None
        if isinstance(v, str):
            v_clean = v.strip()
            if not v_clean or not v_clean.lstrip("-").isdigit():
                return None
            return int(v_clean)
        return int(v) if str(v).strip().lstrip("-").isdigit() else None

    @field_validator("authorized_user_ids", mode="before")
    @classmethod
    def _parse_id_list(cls, v: Any) -> list[int]:
        if isinstance(v, str):
            v_clean = v.strip()
            if not v_clean:
                return []
            return [int(x.strip()) for x in v_clean.split(",") if x.strip().lstrip("-").isdigit()]
        if isinstance(v, (int, float)):
            return [int(v)]
        if isinstance(v, list):
            return [int(x) for x in v if str(x).strip().lstrip("-").isdigit()]
        return []

    @field_validator("max_total_downloads_bytes", mode="before")
    @classmethod
    def _parse_optional_int(cls, v: Any) -> int | None:
        if v == "" or v is None:
            return None
        if isinstance(v, str):
            v_clean = v.strip()
            if not v_clean:
                return None
            return int(v_clean)
        return int(v) if v is not None else None

    @field_validator("magnetio_rpc_secret", "pixeldrain_api_key", "gofile_api_key", mode="before")
    @classmethod
    def _parse_optional_str(cls, v: Any) -> str | None:
        if isinstance(v, str):
            v_clean = v.strip()
            return v_clean if v_clean else None
        return v

    @field_validator("data_dir", "auth_dir", "log_dir")
    @classmethod
    def _ensure_dir(cls, v: Path) -> Path:
        v.mkdir(parents=True, exist_ok=True)
        return v

    @field_validator("pixeldrain_domain")
    @classmethod
    def _validate_pixeldrain_domain(cls, v: str) -> str:
        v_clean = v.strip().lower()
        if v_clean in ("pixeldrain.com", "pixeldra.in"):
            return v_clean
        return "pixeldrain.com"

    @property
    def db_path(self) -> Path:
        return self.data_dir / "state.sqlite3"

    @property
    def gdl_archive_path(self) -> Path:
        return self.data_dir / "gdl_archive.sqlite3"

    @property
    def cdl_archive_path(self) -> Path:
        return self.data_dir / "cdl_archive.db"

    @property
    def downloads_dir(self) -> Path:
        d = self.data_dir / "downloads"
        d.mkdir(parents=True, exist_ok=True)
        return d

    def validate_credentials(self) -> None:
        """Validate that Telegram credentials are provided before starting the bot."""
        missing = []
        if not self.tg_api_id:
            missing.append("TG_API_ID")
        if not self.tg_api_hash:
            missing.append("TG_API_HASH")
        if not self.tg_bot_token:
            missing.append("TG_BOT_TOKEN")
        if missing:
            raise ValueError(f"Missing required environment variables: {', '.join(missing)}")

    def get_user_keystore_info(self, user_id: int | None = None) -> dict[str, Any] | None:
        """Looks up JKS keystore for a given user ID, or falls back to global settings."""
        import json
        import shutil
        if user_id:
            user_dir = (self.auth_dir / str(user_id)).resolve()
            # Migration check: if Pyrogram saved to data/auth/<user_id>, move files to auth/<user_id>
            data_user_dir = (self.data_dir / "auth" / str(user_id)).resolve()
            if data_user_dir.is_dir() and data_user_dir != user_dir:
                user_dir.mkdir(parents=True, exist_ok=True)
                for item in data_user_dir.iterdir():
                    if item.is_file():
                        target_path = user_dir / item.name
                        if not target_path.exists():
                            try:
                                shutil.move(str(item), str(target_path))
                            except Exception:
                                pass
                try:
                    shutil.rmtree(data_user_dir, ignore_errors=True)
                except Exception:
                    pass

            cfg_path = user_dir / "keystore_config.json"
            if cfg_path.is_file():
                try:
                    cfg = json.loads(cfg_path.read_text(encoding="utf-8"))
                    if cfg.get("store_pass") and cfg.get("key_alias"):
                        ks_files = sorted(list(user_dir.glob("*.jks")) + list(user_dir.glob("*.keystore")))
                        if ks_files:
                            return {
                                "keystore_path": ks_files[0].resolve(),
                                "store_pass": cfg["store_pass"],
                                "key_alias": cfg["key_alias"],
                                "key_pass": cfg.get("key_pass") or cfg["store_pass"],
                            }
                except Exception as e:
                    log.warning("Failed loading user keystore config for user %s: %s", user_id, e)

        # Fallback to global settings
        if self.keystore_path:
            p = Path(self.keystore_path).resolve()
            if p.is_file():
                return {
                    "keystore_path": p,
                    "store_pass": self.keystore_pass or "",
                    "key_alias": self.key_alias or "",
                    "key_pass": self.key_pass or self.keystore_pass or "",
                }

        # Check default fallback locations in auth/data
        for fallback_p in (self.auth_dir / "keystore.jks", self.data_dir / "keystore.jks"):
            p = fallback_p.resolve()
            if p.is_file():
                return {
                    "keystore_path": p,
                    "store_pass": self.keystore_pass or "",
                    "key_alias": self.key_alias or "",
                    "key_pass": self.key_pass or self.keystore_pass or "",
                }

        return None


settings = Settings()

