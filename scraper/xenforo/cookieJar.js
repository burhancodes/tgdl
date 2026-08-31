import fs from 'fs';
import path from 'path';

/**
 * Netscape cookies.txt parser and CookieJar for managing user_id specific cookies.
 */
export class CookieJar {
  constructor() {
    /** @type {Array<{domain: string, includeSubdomains: boolean, path: string, secure: boolean, expires: number, name: string, value: string}>} */
    this.cookies = [];
  }

  /**
   * Parse Netscape cookies.txt formatted string.
   * @param {string} content
   */
  parseNetscape(content) {
    if (!content || typeof content !== 'string') return;

    const lines = content.split(/\r?\n/);
    for (const line of lines) {
      const trimmed = line.trim();
      if (!trimmed || trimmed.startsWith('#')) {
        // HTTP-only cookies sometimes start with #HttpOnly_
        if (trimmed.startsWith('#HttpOnly_')) {
          const cleanLine = trimmed.slice('#HttpOnly_'.length).trim();
          this._parseCookieLine(cleanLine);
        }
        continue;
      }
      this._parseCookieLine(trimmed);
    }
  }

  /**
   * @private
   */
  _parseCookieLine(line) {
    const parts = line.split('\t');
    if (parts.length < 7) {
      // Fallback for space-separated or malformed lines
      const spaceParts = line.split(/\s+/);
      if (spaceParts.length >= 7) {
        this._addCookieRecord({
          domain: spaceParts[0],
          includeSubdomains: spaceParts[1].toUpperCase() === 'TRUE',
          path: spaceParts[2] || '/',
          secure: spaceParts[3].toUpperCase() === 'TRUE',
          expires: parseInt(spaceParts[4], 10) || 0,
          name: spaceParts[5],
          value: spaceParts.slice(6).join(' '),
        });
      }
      return;
    }

    this._addCookieRecord({
      domain: parts[0],
      includeSubdomains: parts[1].toUpperCase() === 'TRUE',
      path: parts[2] || '/',
      secure: parts[3].toUpperCase() === 'TRUE',
      expires: parseInt(parts[4], 10) || 0,
      name: parts[5],
      value: parts.slice(6).join('\t'),
    });
  }

  /**
   * @private
   */
  _addCookieRecord(cookie) {
    if (!cookie.name) return;
    // Normalize domain
    let domain = cookie.domain.toLowerCase().trim();
    if (domain.startsWith('.')) {
      domain = domain.slice(1);
      cookie.includeSubdomains = true;
    }
    cookie.domain = domain;

    // Replace existing cookie with same name, domain, and path
    this.cookies = this.cookies.filter(
      c => !(c.name === cookie.name && c.domain === cookie.domain && c.path === cookie.path)
    );
    this.cookies.push(cookie);
  }

  /**
   * Add a single cookie or Set-Cookie header value.
   * @param {string} urlStr
   * @param {string} setCookieHeader
   */
  setCookie(urlStr, setCookieHeader) {
    if (!setCookieHeader) return;
    try {
      const url = new URL(urlStr);
      const parts = setCookieHeader.split(';').map(p => p.trim());
      if (!parts.length) return;

      const [nameVal, ...attrs] = parts;
      const eqIdx = nameVal.indexOf('=');
      if (eqIdx <= 0) return;

      const name = nameVal.slice(0, eqIdx).trim();
      const value = nameVal.slice(eqIdx + 1).trim();

      let domain = url.hostname.toLowerCase();
      let cookiePath = '/';
      let secure = url.protocol === 'https:';
      let expires = 0;
      let includeSubdomains = false;

      for (const attr of attrs) {
        const [k, v] = attr.split('=').map(s => s.trim());
        const kLower = k.toLowerCase();
        if (kLower === 'domain' && v) {
          domain = v.toLowerCase().startsWith('.') ? v.slice(1).toLowerCase() : v.toLowerCase();
          includeSubdomains = true;
        } else if (kLower === 'path' && v) {
          cookiePath = v;
        } else if (kLower === 'secure') {
          secure = true;
        } else if (kLower === 'max-age' && v) {
          expires = Math.floor(Date.now() / 1000) + parseInt(v, 10);
        } else if (kLower === 'expires' && v) {
          const d = new Date(v);
          if (!isNaN(d.getTime())) {
            expires = Math.floor(d.getTime() / 1000);
          }
        }
      }

      this._addCookieRecord({
        domain,
        includeSubdomains,
        path: cookiePath,
        secure,
        expires,
        name,
        value,
      });
    } catch (e) {
      // Ignore invalid URLs
    }
  }

  /**
   * Get formatted Cookie header for a given target URL.
   * @param {string} urlStr
   * @returns {string}
   */
  getCookieHeader(urlStr) {
    if (!urlStr) return '';
    try {
      const url = new URL(urlStr);
      const hostname = url.hostname.toLowerCase();
      const pathname = url.pathname || '/';
      const isHttps = url.protocol === 'https:';
      const nowSec = Math.floor(Date.now() / 1000);

      const matching = this.cookies.filter(cookie => {
        // Expiry check (0 means session cookie)
        if (cookie.expires > 0 && cookie.expires < nowSec) {
          return false;
        }

        // HTTPS check
        if (cookie.secure && !isHttps) {
          return false;
        }

        // Domain check
        const cookieDomain = cookie.domain.toLowerCase();
        if (hostname === cookieDomain) {
          // Exact match
        } else if (cookie.includeSubdomains && hostname.endsWith(`.${cookieDomain}`)) {
          // Subdomain match
        } else {
          return false;
        }

        // Path check
        if (!pathname.startsWith(cookie.path)) {
          return false;
        }

        return true;
      });

      return matching.map(c => `${c.name}=${c.value}`).join('; ');
    } catch (e) {
      return '';
    }
  }

  /**
   * Get specific cookie value for a given URL and name.
   * @param {string} urlStr
   * @param {string} name
   * @returns {string|null}
   */
  getCookieValue(urlStr, name) {
    const header = this.getCookieHeader(urlStr);
    if (!header) return null;
    const parts = header.split(';').map(p => p.trim());
    for (const part of parts) {
      const [k, ...v] = part.split('=');
      if (k.trim() === name) {
        return v.join('=');
      }
    }
    return null;
  }
}

/**
 * Resolves cookies.txt file path for a user_id or global fallback.
 * Checks in order:
 * 1. auth/{user_id}/cookies.txt
 * 2. auth/cookies.txt
 * 3. ./cookies.txt
 *
 * @param {string|number|null} userId
 * @param {string} [baseDir]
 * @returns {string|null}
 */
export function getCookiesPath(userId = null, baseDir = process.cwd()) {
  const candidates = [];
  if (userId) {
    candidates.push(path.resolve(baseDir, 'auth', String(userId), 'cookies.txt'));
    candidates.push(path.resolve(baseDir, '..', 'auth', String(userId), 'cookies.txt'));
  }
  candidates.push(path.resolve(baseDir, 'auth', 'cookies.txt'));
  candidates.push(path.resolve(baseDir, '..', 'auth', 'cookies.txt'));
  candidates.push(path.resolve(baseDir, 'cookies.txt'));
  candidates.push(path.resolve(baseDir, '..', 'cookies.txt'));

  for (const candidate of candidates) {
    try {
      if (fs.existsSync(candidate) && fs.statSync(candidate).isFile()) {
        return candidate;
      }
    } catch (e) {
      // Continue
    }
  }
  return null;
}

/**
 * Creates and loads a CookieJar instance for a specific user_id or raw cookie string.
 * @param {{userId?: string|number, cookiesTxt?: string, baseDir?: string}} [opts]
 * @returns {CookieJar}
 */
export function createCookieJar({ userId = null, cookiesTxt = null, baseDir = process.cwd() } = {}) {
  const jar = new CookieJar();

  if (cookiesTxt && typeof cookiesTxt === 'string') {
    jar.parseNetscape(cookiesTxt);
  }

  if (userId || !cookiesTxt) {
    const filePath = getCookiesPath(userId, baseDir);
    if (filePath) {
      try {
        const content = fs.readFileSync(filePath, 'utf8');
        jar.parseNetscape(content);
      } catch (err) {
        // Log silently or ignore
      }
    }
  }

  return jar;
}
