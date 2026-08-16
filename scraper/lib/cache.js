import { logger } from './logger.js';

// Default maximum memory cache size: 20 MB
const MAX_CACHE_BYTES = parseInt(process.env.CACHE_MAX_BYTES, 10) || (20 * 1024 * 1024);

class BoundedLRUCache {
  constructor(maxBytes = MAX_CACHE_BYTES) {
    this.maxBytes = maxBytes;
    this.currentBytes = 0;
    this.map = new Map(); // key -> { value, size, expiresAt }

    // Periodically sweep expired keys every 5 minutes
    this._sweepInterval = setInterval(() => this.sweepExpired(), 5 * 60 * 1000);
    if (this._sweepInterval.unref) {
      this._sweepInterval.unref();
    }
  }

  _estimateSize(key, value) {
    try {
      const jsonStr = JSON.stringify(value);
      return (jsonStr ? Buffer.byteLength(jsonStr, 'utf8') : 0) + Buffer.byteLength(String(key), 'utf8') + 64;
    } catch {
      return 1024;
    }
  }

  get(key) {
    const entry = this.map.get(key);
    if (!entry) return undefined;

    if (entry.expiresAt && Date.now() > entry.expiresAt) {
      this.delete(key);
      return undefined;
    }

    // Refresh LRU access order: delete and re-insert at end
    this.map.delete(key);
    this.map.set(key, entry);
    return entry.value;
  }

  set(key, value, ttlMs = 3600 * 1000) {
    const entrySize = this._estimateSize(key, value);
    if (entrySize > this.maxBytes) {
      // Single entry exceeds maximum total cache size; skip caching
      return;
    }

    // If key exists, delete old entry to adjust currentBytes
    if (this.map.has(key)) {
      this.delete(key);
    }

    // Evict oldest entries until within maxBytes limit
    while (this.currentBytes + entrySize > this.maxBytes && this.map.size > 0) {
      const oldestKey = this.map.keys().next().value;
      if (oldestKey !== undefined) {
        this.delete(oldestKey);
      } else {
        break;
      }
    }

    const expiresAt = ttlMs > 0 ? Date.now() + ttlMs : null;
    this.map.set(key, { value, size: entrySize, expiresAt });
    this.currentBytes += entrySize;
  }

  has(key) {
    return this.get(key) !== undefined;
  }

  delete(key) {
    const entry = this.map.get(key);
    if (entry) {
      this.currentBytes = Math.max(0, this.currentBytes - entry.size);
      this.map.delete(key);
      return true;
    }
    return false;
  }

  clear() {
    this.map.clear();
    this.currentBytes = 0;
  }

  sweepExpired() {
    const now = Date.now();
    for (const [key, entry] of this.map.entries()) {
      if (entry.expiresAt && now > entry.expiresAt) {
        this.delete(key);
      }
    }
  }

  get stats() {
    return {
      entries: this.map.size,
      usedBytes: this.currentBytes,
      maxBytes: this.maxBytes,
      usedMB: (this.currentBytes / (1024 * 1024)).toFixed(2),
      maxMB: (this.maxBytes / (1024 * 1024)).toFixed(0),
    };
  }
}

const _cache = new BoundedLRUCache(MAX_CACHE_BYTES);
logger.info(`Scraper cache: in-memory bounded LRU (max size: ${(MAX_CACHE_BYTES / (1024 * 1024)).toFixed(0)}MB)`);

export function getStore() {
  return _cache;
}

export async function cacheHas(key) {
  try {
    return _cache.has(key);
  } catch (err) {
    logger.error(`Cache error on cacheHas: ${err?.message || err}`);
    return false;
  }
}

export async function cacheWrap(key, loader, ttlSeconds = 3600) {
  try {
    const hit = _cache.get(key);
    if (hit !== undefined) {
      return { value: hit, cached: true };
    }
  } catch (err) {
    logger.error(`Cache error on cacheWrap get: ${err?.message || err}`);
  }

  const value = await loader();
  const isEmpty = Array.isArray(value) && value.length === 0;

  if (!isEmpty && value !== null && value !== undefined) {
    try {
      _cache.set(key, value, ttlSeconds * 1000);
    } catch (err) {
      logger.error(`Cache error on cacheWrap set: ${err?.message || err}`);
    }
  }

  return { value, cached: false };
}
