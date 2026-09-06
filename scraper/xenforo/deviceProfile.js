import fs from 'fs';
import path from 'path';

export const DEFAULT_FALLBACK_UA =
  'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36';

/**
 * Extracts User-Agent string from Netscape cookies.txt comment lines or headers.
 * Supported patterns:
 * # User-Agent: Mozilla/5.0 ...
 * # UA: Mozilla/5.0 ...
 * # UserAgent: Mozilla/5.0 ...
 * # Browser: Mozilla/5.0 ...
 *
 * @param {string} text
 * @returns {string|null}
 */
export function extractUserAgentFromCookies(text) {
  if (!text || typeof text !== 'string') return null;

  const lines = text.split(/\r?\n/);
  for (const line of lines) {
    const trimmed = line.trim();
    if (trimmed.startsWith('#')) {
      const match = trimmed.match(/^#\s*(?:user-agent|useragent|ua|browser|device)\s*:\s*(.+)$/i);
      if (match && match[1]) {
        const ua = match[1].trim();
        if (ua.length > 10 && ua.includes('/')) {
          return ua;
        }
      }
    }
  }
  return null;
}

/**
 * Resolves user-agent.txt file path for a user_id or global fallback.
 * Checks:
 * 1. auth/{user_id}/user-agent.txt (or ua.txt)
 * 2. auth/user-agent.txt (or ua.txt)
 *
 * @param {string|number|null} userId
 * @param {string} [baseDir]
 * @returns {string|null}
 */
export function getUserAgentPath(userId = null, baseDir = process.cwd()) {
  const candidates = [];
  if (userId) {
    candidates.push(path.resolve(baseDir, 'auth', String(userId), 'user-agent.txt'));
    candidates.push(path.resolve(baseDir, 'auth', String(userId), 'ua.txt'));
    candidates.push(path.resolve(baseDir, '..', 'auth', String(userId), 'user-agent.txt'));
    candidates.push(path.resolve(baseDir, '..', 'auth', String(userId), 'ua.txt'));
  }
  candidates.push(path.resolve(baseDir, 'auth', 'user-agent.txt'));
  candidates.push(path.resolve(baseDir, 'auth', 'ua.txt'));
  candidates.push(path.resolve(baseDir, '..', 'auth', 'user-agent.txt'));
  candidates.push(path.resolve(baseDir, '..', 'auth', 'ua.txt'));

  for (const candidate of candidates) {
    try {
      if (fs.existsSync(candidate) && fs.statSync(candidate).isFile()) {
        return candidate;
      }
    } catch (e) {
      // Ignore
    }
  }
  return null;
}

/**
 * Analyzes a User-Agent string to determine browser engine, version, OS, and platform hints.
 *
 * @param {string} uaString
 * @returns {object}
 */
export function parseUserAgent(uaString) {
  const ua = (uaString || '').trim() || DEFAULT_FALLBACK_UA;

  const isMobile = /Android|iPhone|iPad|iPod|Mobile|webOS|BlackBerry|IEMobile|Opera Mini/i.test(ua);
  let platform = 'Windows';
  let platformVersion = '10.0.0';

  if (/Windows/i.test(ua)) {
    platform = 'Windows';
    if (/Windows NT 10\.0/i.test(ua)) platformVersion = '10.0.0';
    else if (/Windows NT 6\.3/i.test(ua)) platformVersion = '8.1.0';
    else if (/Windows NT 6\.1/i.test(ua)) platformVersion = '7.0.0';
  } else if (/Macintosh|Mac OS X/i.test(ua)) {
    platform = 'macOS';
    const macMatch = ua.match(/Mac OS X (\d+[._]\d+[._]\d+)/i);
    if (macMatch) platformVersion = macMatch[1].replace(/_/g, '.');
    else platformVersion = '14.0.0';
  } else if (/Android/i.test(ua)) {
    platform = 'Android';
    const andMatch = ua.match(/Android (\d+(\.\d+)*)/i);
    if (andMatch) platformVersion = andMatch[1];
    else platformVersion = '14.0.0';
  } else if (/iPhone|iPad|iPod/i.test(ua)) {
    platform = 'iOS';
    platformVersion = '17.0';
  } else if (/Linux/i.test(ua)) {
    platform = 'Linux';
    platformVersion = '6.5.0';
  }

  // Browser engine & brand detection
  let engine = 'Chromium';
  let brand = 'Google Chrome';
  let majorVersion = '133';
  let fullVersion = '133.0.0.0';

  const edgeMatch = ua.match(/Edg\/(\d+)(\.[\d.]+)/i);
  const operaMatch = ua.match(/(?:OPR|Opera)\/(\d+)(\.[\d.]+)/i);
  const chromeMatch = ua.match(/Chrome\/(\d+)(\.[\d.]+)/i);
  const firefoxMatch = ua.match(/Firefox\/(\d+)(\.[\d.]+)/i);
  const safariMatch = !chromeMatch && !firefoxMatch && ua.match(/Version\/(\d+)(\.[\d.]+).*Safari/i);

  if (edgeMatch) {
    engine = 'Chromium';
    brand = 'Microsoft Edge';
    majorVersion = edgeMatch[1];
    fullVersion = `${edgeMatch[1]}${edgeMatch[2]}`;
  } else if (operaMatch) {
    engine = 'Chromium';
    brand = 'Opera';
    majorVersion = operaMatch[1];
    fullVersion = `${operaMatch[1]}${operaMatch[2]}`;
  } else if (chromeMatch) {
    engine = 'Chromium';
    brand = 'Google Chrome';
    majorVersion = chromeMatch[1];
    fullVersion = `${chromeMatch[1]}${chromeMatch[2]}`;
  } else if (firefoxMatch) {
    engine = 'Gecko';
    brand = 'Firefox';
    majorVersion = firefoxMatch[1];
    fullVersion = `${firefoxMatch[1]}${firefoxMatch[2]}`;
  } else if (safariMatch) {
    engine = 'WebKit';
    brand = 'Safari';
    majorVersion = safariMatch[1];
    fullVersion = `${safariMatch[1]}${safariMatch[2]}`;
  }

  return {
    userAgent: ua,
    engine,
    brand,
    majorVersion,
    fullVersion,
    platform,
    platformVersion,
    isMobile,
  };
}

/**
 * Generates exact matching HTTP headers based on the parsed device profile.
 * - Chromium: Injects Sec-CH-UA, Sec-CH-UA-Platform, Sec-CH-UA-Mobile, Sec-Fetch-*
 * - Firefox: Omits Sec-CH-UA (standard Firefox does not send CH) to avoid bot detection.
 * - Safari: Omits Sec-CH-UA and uses WebKit headers.
 *
 * @param {string} uaString
 * @param {object} [opts]
 * @param {string} [opts.destType='document'] - 'document' | 'image' | 'video' | 'json'
 * @param {string} [opts.referer]
 * @returns {Record<string, string>}
 */
export function getDeviceHeaders(uaString, { destType = 'document', referer = '' } = {}) {
  const profile = parseUserAgent(uaString);
  const headers = {
    'User-Agent': profile.userAgent,
    'Accept-Language': 'en-US,en;q=0.9',
  };

  if (referer) {
    headers['Referer'] = referer;
  }

  if (destType === 'image') {
    headers['Accept'] = 'image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8';
    headers['Sec-Fetch-Dest'] = 'image';
    headers['Sec-Fetch-Mode'] = 'no-cors';
    headers['Sec-Fetch-Site'] = 'cross-site';
  } else if (destType === 'video') {
    headers['Accept'] = '*/*';
    headers['Sec-Fetch-Dest'] = 'video';
    headers['Sec-Fetch-Mode'] = 'no-cors';
    headers['Sec-Fetch-Site'] = 'cross-site';
  } else if (destType === 'json') {
    headers['Accept'] = 'application/json, text/plain, */*';
    headers['Sec-Fetch-Dest'] = 'empty';
    headers['Sec-Fetch-Mode'] = 'cors';
    headers['Sec-Fetch-Site'] = 'same-origin';
  } else {
    // Default document navigation
    headers['Accept'] = 'text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8';
    headers['Upgrade-Insecure-Requests'] = '1';
    headers['Sec-Fetch-Dest'] = 'document';
    headers['Sec-Fetch-Mode'] = 'navigate';
    headers['Sec-Fetch-Site'] = 'same-origin';
    headers['Sec-Fetch-User'] = '?1';
  }

  // Inject Client Hints only for Chromium-based engines
  if (profile.engine === 'Chromium') {
    const secChUa = profile.brand === 'Microsoft Edge'
      ? `"Not(A:Brand";v="99", "Microsoft Edge";v="${profile.majorVersion}", "Chromium";v="${profile.majorVersion}"`
      : profile.brand === 'Opera'
      ? `"Not(A:Brand";v="99", "Opera";v="${profile.majorVersion}", "Chromium";v="${profile.majorVersion}"`
      : `"Not(A:Brand";v="99", "Google Chrome";v="${profile.majorVersion}", "Chromium";v="${profile.majorVersion}"`;

    headers['sec-ch-ua'] = secChUa;
    headers['sec-ch-ua-mobile'] = profile.isMobile ? '?1' : '?0';
    headers['sec-ch-ua-platform'] = `"${profile.platform}"`;
  }

  return headers;
}

/**
 * Resolves the effective device User-Agent for a user session.
 *
 * @param {object} opts
 * @param {string|number|null} [opts.userId]
 * @param {string|null} [opts.cookiesTxt]
 * @param {string|null} [opts.userAgent]
 * @param {string} [opts.baseDir]
 * @returns {string}
 */
export function resolveUserAgent({ userId = null, cookiesTxt = null, userAgent = null, baseDir = process.cwd() } = {}) {
  // 1. Explicit userAgent
  if (userAgent && typeof userAgent === 'string' && userAgent.trim().length > 10) {
    return userAgent.trim();
  }

  // 2. Extracted from cookiesTxt parameter
  if (cookiesTxt && typeof cookiesTxt === 'string') {
    const extracted = extractUserAgentFromCookies(cookiesTxt);
    if (extracted) return extracted;
  }

  // 3. User cookies file on disk
  if (userId) {
    const userCookiesPath = path.resolve(baseDir, 'auth', String(userId), 'cookies.txt');
    try {
      if (fs.existsSync(userCookiesPath)) {
        const content = fs.readFileSync(userCookiesPath, 'utf8');
        const extracted = extractUserAgentFromCookies(content);
        if (extracted) return extracted;
      }
    } catch (e) {
      // Ignore
    }
  }

  // 4. auth/{user_id}/user-agent.txt file
  const uaFilePath = getUserAgentPath(userId, baseDir);
  if (uaFilePath) {
    try {
      const content = fs.readFileSync(uaFilePath, 'utf8').trim();
      if (content.length > 10) return content;
    } catch (e) {
      // Ignore
    }
  }

  // 5. Global auth/cookies.txt
  const globalCookiesPath = path.resolve(baseDir, 'auth', 'cookies.txt');
  try {
    if (fs.existsSync(globalCookiesPath)) {
      const content = fs.readFileSync(globalCookiesPath, 'utf8');
      const extracted = extractUserAgentFromCookies(content);
      if (extracted) return extracted;
    }
  } catch (e) {
    // Ignore
  }

  return DEFAULT_FALLBACK_UA;
}
