import fs from 'fs';
import path from 'path';
import {
  DEFAULT_FALLBACK_UA,
  extractUserAgentFromCookies,
  getDeviceHeaders,
  parseUserAgent,
  resolveUserAgent,
} from './deviceProfile.js';

/**
 * Netscape cookies.txt parser and CookieJar for managing user_id specific cookies
 * and device fingerprint / spoofing headers.
 */
export class CookieJar {
  constructor() {
    /** @type {Array<{domain: string, includeSubdomains: boolean, path: string, secure: boolean, expires: number, name: string, value: string}>} */
    this.cookies = [];
    /** @type {string} */
    this.userAgent = DEFAULT_FALLBACK_UA;
  }

  /**
   * Set the spoofed device User-Agent.
   * @param {string} ua
   */
  setUserAgent(ua) {
    if (ua && typeof ua === 'string' && ua.trim().length > 10) {
      this.userAgent = ua.trim();
    }
  }

  /**
   * Get current spoofed device User-Agent.
   * @returns {string}
   */
  getUserAgent() {
    return this.userAgent;
  }

  /**
   * Returns parsed device profile for current User-Agent.
   * @returns {object}
   */
  getDeviceProfile() {
    return parseUserAgent(this.userAgent);
  }

  /**
   * Generates exact matching HTTP headers based on the spoofed device and target URL.
   * @param {string} [urlStr]
   * @param {object} [opts]
   * @param {string} [opts.destType='document']
   * @param {string} [opts.referer]
   * @param {Record<string, string>} [opts.extraHeaders]
   * @returns {Record<string, string>}
   */
  getDeviceHeaders(urlStr = '', { destType = 'document', referer = '', extraHeaders = {} } = {}) {
    const baseHeaders = getDeviceHeaders(this.userAgent, {
      destType,
      referer: referer || urlStr || '',
    });
    const cookieHeader = urlStr ? this.getCookieHeader(urlStr) : '';
    return {
      ...baseHeaders,
      ...(cookieHeader ? { Cookie: cookieHeader } : {}),
      ...extraHeaders,
    };
  }

  /**
   * Parse Netscape cookies.txt formatted string.
   * Automatically extracts and saves spoofed User-Agent if present in comment lines.
   * @param {string} content
   */
  parseNetscape(content) {
    if (!content || typeof content !== 'string') return;

    // Extract User-Agent if present in comments
    const extractedUa = extractUserAgentFromCookies(content);
    if (extractedUa) {
      this.setUserAgent(extractedUa);
    }

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
 * Creates and loads a CookieJar instance for a specific user_id or raw cookie string
 * with automatic device fingerprint & User-Agent resolution.
 * @param {{userId?: string|number, cookiesTxt?: string, userAgent?: string, baseDir?: string}} [opts]
 * @returns {CookieJar}
 */
export function createCookieJar({ userId = null, cookiesTxt = null, userAgent = null, baseDir = process.cwd() } = {}) {
  const jar = new CookieJar();

  // 1. Resolve effective User-Agent from explicit param, cookies, files, or fallback
  const resolvedUa = resolveUserAgent({ userId, cookiesTxt, userAgent, baseDir });
  jar.setUserAgent(resolvedUa);

  // 2. Parse raw cookies text if provided
  if (cookiesTxt && typeof cookiesTxt === 'string') {
    jar.parseNetscape(cookiesTxt);
  }

  // 3. Load cookies from disk if available
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

  // If explicit userAgent was passed, guarantee it overrides any comment UA
  if (userAgent && typeof userAgent === 'string' && userAgent.trim().length > 10) {
    jar.setUserAgent(userAgent.trim());
  }

  return jar;
}
