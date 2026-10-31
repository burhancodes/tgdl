import http from 'node:http';
import https from 'node:https';
import axios from 'axios';
import Bottleneck from 'bottleneck';
import { recordSuccess, recordFailure } from './providerHealth.js';
import { proxyAppliesTo, proxyAgents } from './proxy.js';

const DEFAULT_HEADERS = {
  'User-Agent':
    'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 ' +
    '(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36',
  'Accept-Language': 'en-US,en;q=0.9',
  'Accept': 'text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8',
};

const limiters = new Map();

function getLimiter(key) {
  if (!limiters.has(key)) {
    limiters.set(key, new Bottleneck({
      minTime: 200,
      maxConcurrent: 2,
    }));
  }
  return limiters.get(key);
}

export async function get(url, opts = {}) {
  return request('get', url, opts);
}

export async function post(url, opts = {}) {
  return request('post', url, opts);
}

async function request(method, url, {
  limiterKey = 'default',
  params = {},
  headers = {},
  timeout = 12000,
  responseType = 'text',
  retries = 2,
  lookup = null,
  data = undefined,
} = {}) {
  const limiter = getLimiter(limiterKey);

  const safe = lookup
    ? {
        httpAgent: new http.Agent({ lookup, keepAlive: false }),
        httpsAgent: new https.Agent({ lookup, keepAlive: false }),
        maxRedirects: 0,
      }
    : (proxyAppliesTo(limiterKey) ? (proxyAgents() ?? {}) : {});

  return limiter.schedule(async () => {
    let lastErr;
    for (let attempt = 0; attempt <= retries; attempt++) {
      try {
        const res = await axios({
          method,
          url,
          ...(data !== undefined ? { data } : {}),
          params,
          headers: { ...DEFAULT_HEADERS, ...headers },
          timeout,
          responseType,
          ...safe,
        });
        recordSuccess(limiterKey);
        return res;
      } catch (err) {
        lastErr = err;
        const status = err.response?.status;
        if (status === 429 && attempt < retries) {
          await sleep(retryAfterMs(err, attempt));
          continue;
        }
        if (status && status < 500) {
          recordFailure(limiterKey, err);
          throw err;
        }
        if (attempt < retries) await sleep(1000 * (attempt + 1));
      }
    }
    recordFailure(limiterKey, lastErr);
    throw lastErr;
  });
}

function sleep(ms) {
  return new Promise(r => setTimeout(r, ms));
}

const RETRY_AFTER_MAX_MS = 5000;

export function retryAfterMs(err, attempt = 0) {
  const fallback = 1000 * (attempt + 1);
  const header = err?.response?.headers?.['retry-after'];
  if (header == null) return fallback;

  const trimmed = String(header).trim();
  if (!trimmed) return fallback;
  const seconds = Number(trimmed);
  if (Number.isFinite(seconds)) {
    return seconds < 0 ? fallback : Math.min(seconds * 1000, RETRY_AFTER_MAX_MS);
  }

  const date = Date.parse(header);
  if (!Number.isNaN(date)) {
    return Math.min(Math.max(0, date - Date.now()), RETRY_AFTER_MAX_MS);
  }

  return fallback;
}
