from __future__ import annotations

import json
import logging
import os
import re
import shutil
import subprocess
import time
import urllib.request
from pathlib import Path
from typing import Any

from ...config import settings

log = logging.getLogger(__name__)

# Hardcoded User-Agent from Chromium on Linux (suitable for VPS deployments)
DEFAULT_USER_AGENT = (
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.7922.71 Safari/537.36"
)
DEFAULT_FALLBACK_SALT = "12af056dacea0b"
GOFILE_WT_URL = "https://gofile.io/js/wt.obf.js"
GOFILE_HOME_URL = "https://gofile.io/"


def get_browser_user_agent() -> str:
    """
    Returns the configured or discovered browser User-Agent.
    Defaults to the hardcoded modern Chromium User-Agent for headless / VPS environments,
    with automatic system browser inspection fallback if available.
    """
    env_ua = os.environ.get("GOFILE_USER_AGENT") or os.environ.get("BROWSER_USER_AGENT")
    if env_ua and env_ua.strip():
        return env_ua.strip()

    # If installed browser binary has a newer version, detect it; otherwise default to hardcoded UA
    for bin_name in ["chromium", "chromium-browser", "google-chrome", "brave-browser"]:
        bin_path = shutil.which(bin_name)
        if bin_path:
            try:
                res = subprocess.run([bin_path, "--version"], capture_output=True, text=True, timeout=3)
                if res.returncode == 0 and res.stdout:
                    match = re.search(r"(\d+\.\d+\.\d+\.\d+)", res.stdout)
                    if match:
                        ver = match.group(1)
                        return f"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/{ver} Safari/537.36"
            except Exception:
                pass

    for bin_name in ["firefox", "firefox-esr"]:
        bin_path = shutil.which(bin_name)
        if bin_path:
            try:
                res = subprocess.run([bin_path, "--version"], capture_output=True, text=True, timeout=3)
                if res.returncode == 0 and res.stdout:
                    match = re.search(r"Mozilla Firefox (\d+\.\d+)", res.stdout)
                    if match:
                        ver = match.group(1)
                        return f"Mozilla/5.0 (X11; Linux x86_64; rv:{ver}) Gecko/20100101 Firefox/{ver}"
            except Exception:
                pass

    return DEFAULT_USER_AGENT


def _extract_salt_via_js_runtime(js_code: str) -> str | None:
    """Executes wt.obf.js in an isolated Node.js / Deno VM to extract the salt parameter."""
    node_bin = shutil.which("node") or shutil.which("deno")
    if not node_bin:
        return None

    try:
        script = f"""
const vm = require("vm");
const jsCode = {json.dumps(js_code)};
const ctx = {{
    navigator: {{ userAgent: "UA", language: "en-US" }},
    Date: {{ now: () => 1700000000000 }},
    window: {{}},
    console: console
}};
vm.createContext(ctx);
vm.runInContext(jsCode, ctx);
let extractedSalt = null;
ctx._sha256 = function(str) {{
    const parts = String(str).split("::");
    if (parts.length >= 5) {{
        extractedSalt = parts[parts.length - 1];
    }}
    return "mock_digest";
}};
if (typeof ctx.generateWT === "function") {{
    ctx.generateWT("token");
}}
if (extractedSalt) {{
    process.stdout.write(extractedSalt);
}}
"""
        res = subprocess.run(
            [node_bin, "-e", script],
            capture_output=True,
            text=True,
            timeout=5,
        )
        if res.returncode == 0 and res.stdout.strip():
            extracted = res.stdout.strip()
            if re.fullmatch(r"[a-fA-F0-9]{8,64}", extracted):
                return extracted
    except Exception as e:
        log.debug("Failed JS runtime salt extraction: %s", e)

    return None


def _extract_salt_via_regex(js_code: str) -> str | None:
    """Fallback static regex/heuristic extraction from wt.obf.js."""
    # 1. Look for salt after '::' delimiter e.g. ::<salt>
    colon_matches = re.findall(r"::([a-f0-9]{12,32})", js_code, re.IGNORECASE)
    if colon_matches:
        for m in colon_matches:
            if m.lower() not in ("9844d94d963d30",):
                return m.lower()

    # 2. Look for hex-like salt strings (12-32 hex chars) quoted in wt.obf.js
    matches = re.findall(r'["\']([a-f0-9]{12,32})["\']', js_code, re.IGNORECASE)
    if matches:
        for m in matches:
            if m.lower() not in ("9844d94d963d30",):
                return m.lower()

    # 3. Look for hex-escaped strings like \x31\x32...
    hex_escapes = re.findall(r"((?:\\x[0-9a-fA-F]{2}){12,32})", js_code)
    for esc in hex_escapes:
        try:
            decoded = bytes.fromhex(esc.replace("\\x", "")).decode("ascii")
            if re.fullmatch(r"[a-f0-9]{12,32}", decoded, re.IGNORECASE) and decoded.lower() not in ("9844d94d963d30",):
                return decoded.lower()
        except Exception:
            pass

    return None


def fetch_gofile_salt(timeout: float = 10.0) -> str | None:
    """
    Fetches GoFile's client token script (wt.obf.js) and dynamically extracts the active salt.
    """
    ua = get_browser_user_agent()
    urls_to_try = [
        GOFILE_WT_URL,
        "https://gofile.io/dist/js/wt.obf.js",
    ]

    # Try detecting script URL from homepage if available
    try:
        req = urllib.request.Request(GOFILE_HOME_URL, headers={"User-Agent": ua})
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            html = resp.read().decode(errors="ignore")
            match = re.search(r'src=["\']([^"\']*wt\.obf\.js[^"\']*)["\']', html)
            if match:
                script_path = match.group(1)
                if script_path.startswith("http"):
                    urls_to_try.insert(0, script_path)
                elif script_path.startswith("/"):
                    urls_to_try.insert(0, f"https://gofile.io{script_path}")
    except Exception as e:
        log.debug("Could not scrape gofile homepage for wt script: %s", e)

    for url in urls_to_try:
        try:
            req = urllib.request.Request(url, headers={"User-Agent": ua})
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                js_code = resp.read().decode(errors="ignore")
                if not js_code or len(js_code) < 100:
                    continue

                salt = _extract_salt_via_js_runtime(js_code)
                if salt:
                    log.info("Extracted GoFile salt via JS runtime: %s", salt)
                    return salt

                salt = _extract_salt_via_regex(js_code)
                if salt:
                    log.info("Extracted GoFile salt via regex fallback: %s", salt)
                    return salt
        except Exception as e:
            log.debug("Failed fetching %s: %s", url, e)

    return None


def update_gdl_conf_gofile(
    config_path: Path | None = None,
    salt: str | None = None,
    user_agent: str | None = None,
) -> bool:
    """
    Safely updates the `extractor.gofile.salt` and `extractor.user-agent` in the specified
    gallery-dl configuration file.
    """
    path = config_path or (Path(__file__).parent / "gallery-dl.conf")
    if not path.exists() or not path.is_file():
        log.warning("Config path %s does not exist", path)
        return False

    raw_content = path.read_text(encoding="utf-8", errors="ignore")
    if not raw_content.strip():
        return False

    final_salt = (salt or os.environ.get("GOFILE_WT_SALT") or DEFAULT_FALLBACK_SALT).strip()
    final_ua = (user_agent or get_browser_user_agent()).strip()

    # 1. Update salt in JSON / config content using regex replacement to preserve comments & formatting
    # Case A: "salt"\s*:\s*("[^"]*"|null)
    if re.search(r'("salt"\s*:\s*)(?:null|"[^"]*")', raw_content):
        updated_content = re.sub(
            r'("salt"\s*:\s*)(?:null|"[^"]*")',
            rf'\g<1>"{final_salt}"',
            raw_content,
            count=1,
        )
    else:
        # If "salt" key is not present under "gofile", insert it
        gofile_block_pattern = r'("gofile"\s*:\s*\{)'
        if re.search(gofile_block_pattern, raw_content):
            updated_content = re.sub(
                gofile_block_pattern,
                rf'\g<1>\n            "salt": "{final_salt}",',
                raw_content,
                count=1,
            )
        else:
            updated_content = raw_content

    # 2. Update user-agent if "user-agent" key exists
    if final_ua:
        if re.search(r'("user-agent"\s*:\s*)(?:null|"auto"|"[^"]*")', updated_content):
            updated_content = re.sub(
                r'("user-agent"\s*:\s*)(?:null|"auto"|"[^"]*")',
                rf'\g<1>"{final_ua}"',
                updated_content,
                count=1,
            )

    try:
        path.write_text(updated_content, encoding="utf-8")
        log.info("Successfully updated gallery-dl config at %s (salt=%s)", path, final_salt)
        return True
    except Exception as e:
        log.exception("Failed writing updated gallery-dl config to %s: %s", path, e)
        return False


def update_all_gdl_configs(
    salt: str | None = None,
    user_agent: str | None = None,
) -> dict[str, bool]:
    """
    Updates global package gallery-dl.conf, settings.gdl_config_path, and any user-specific configs.
    """
    resolved_salt = salt or fetch_gofile_salt() or DEFAULT_FALLBACK_SALT
    resolved_ua = user_agent or get_browser_user_agent()

    results: dict[str, bool] = {}
    seen_paths: set[Path] = set()

    def _process_conf(p: Path) -> None:
        try:
            resolved = p.resolve()
            if resolved in seen_paths:
                return
            seen_paths.add(resolved)
            if p.exists() and p.is_file():
                results[str(p)] = update_gdl_conf_gofile(p, resolved_salt, resolved_ua)
        except Exception as err:
            log.debug("Error checking config path %s: %s", p, err)

    # 1. Package template
    pkg_conf = Path(__file__).parent / "gallery-dl.conf"
    _process_conf(pkg_conf)

    # 2. Configured settings path
    if settings.gdl_config_path:
        _process_conf(settings.gdl_config_path)

    # 3. Global auth config
    auth_global = settings.auth_dir / "gallery-dl.conf"
    _process_conf(auth_global)

    # 4. User-specific configs in auth_dir / <user_id> / gallery-dl.conf
    if settings.auth_dir.exists():
        for user_folder in settings.auth_dir.iterdir():
            if user_folder.is_dir():
                user_conf = user_folder / "gallery-dl.conf"
                _process_conf(user_conf)

    # Also patch runtime gallery_dl module and set environment variable
    patch_gallery_dl_gofile(resolved_salt)

    return results


def patch_gallery_dl_gofile(salt: str | None = None) -> None:
    """
    Patches gallery_dl.extractor.gofile at runtime to support dynamic salt configuration
    and GOFILE_WT_SALT environment variable.
    Also updates site-packages/gallery_dl/extractor/gofile.py on disk if writable so CLI commands benefit.
    """
    final_salt = (salt or os.environ.get("GOFILE_WT_SALT") or DEFAULT_FALLBACK_SALT).strip()
    os.environ["GOFILE_WT_SALT"] = final_salt

    try:
        import gallery_dl.extractor.gofile as gofile_mod

        def _patched_generate_website_token(self, lang="en-US"):
            import hashlib
            import time

            configured_salt = self.config("salt") or os.environ.get("GOFILE_WT_SALT") or DEFAULT_FALLBACK_SALT
            ua = self.session.headers.get("User-Agent") or get_browser_user_agent()
            data = (
                f"{ua}::"
                f"{lang}::"
                f"{self.api_token}::"
                f"{int(time.time() / 14400)}::"
                f"{configured_salt}"
            )
            return hashlib.sha256(data.encode()).hexdigest()

        if hasattr(gofile_mod, "GofileFolderExtractor"):
            gofile_mod.GofileFolderExtractor._generate_website_token = _patched_generate_website_token
            log.debug("In-memory monkeypatch applied to GofileFolderExtractor._generate_website_token")
    except Exception as e:
        log.debug("Could not patch in-memory gallery_dl extractor: %s", e)

    # Attempt to patch installed file in site-packages for CLI invocation
    try:
        import gallery_dl.extractor.gofile as gofile_mod

        mod_file = getattr(gofile_mod, "__file__", None)
        if mod_file and Path(mod_file).exists():
            mod_path = Path(mod_file)
            code = mod_path.read_text(encoding="utf-8", errors="ignore")
            # If code still has obsolete hardcoded salt without self.config
            if '"9844d94d963d30"' in code or 'self.config("salt"' not in code:
                old_func_pattern = r'def _generate_website_token\(self, lang="en-US"\):.*?(?=def |\Z)'
                new_func = (
                    'def _generate_website_token(self, lang="en-US"):\n'
                    '        # https://gofile.io/dist/js/wt.obf.js\n'
                    '        import os\n'
                    f'        salt = self.config("salt") or os.environ.get("GOFILE_WT_SALT", "{final_salt}")\n'
                    '        data = (f"{self.session.headers.get(\'User-Agent\', \'\')}::"\n'
                    '                f"{lang}::"\n'
                    '                f"{self.api_token}::"\n'
                    '                f"{int(time.time() / 14400)}::"\n'
                    '                f"{salt}")\n'
                    '        return hashlib.sha256(data.encode()).hexdigest()\n\n    '
                )
                patched_code = re.sub(old_func_pattern, new_func, code, flags=re.DOTALL)
                if patched_code != code:
                    mod_path.write_text(patched_code, encoding="utf-8")
                    log.info("Patched installed gallery_dl gofile extractor at %s", mod_path)
    except Exception as e:
        log.debug("Could not patch site-packages file for gallery_dl: %s", e)


def sync_gofile_salt(auto_fetch: bool = True) -> tuple[str, dict[str, bool]]:
    """
    High-level entrypoint: fetches the latest salt if requested (or falls back),
    patches gallery-dl, and updates all configuration files.
    """
    salt = None
    if auto_fetch:
        salt = fetch_gofile_salt()
    salt = salt or os.environ.get("GOFILE_WT_SALT") or DEFAULT_FALLBACK_SALT
    results = update_all_gdl_configs(salt=salt)
    return salt, results
