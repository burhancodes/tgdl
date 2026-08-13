import Keyv from 'keyv';
import KeyvRedis from '@keyv/redis';
import { logger } from './logger.js';

let _store = null;
let lastErrorMsg = null;
let lastErrorTime = 0;

function logCacheError(err) {
  const msg = err?.message || String(err);
  const now = Date.now();
  // Suppress repetitive ECONNREFUSED log spam within 60 seconds
  if (msg === lastErrorMsg && (now - lastErrorTime) < 60000) {
    return;
  }
  lastErrorMsg = msg;
  lastErrorTime = now;
  logger.error(`Cache error: ${msg}`);
}

function getStore() {
  if (_store) return _store;

  const redisUri = (process.env.REDIS_URI || process.env.REDIS_URL || '').trim();

  if (redisUri) {
    try {
      const redis = new KeyvRedis(redisUri);
      _store = new Keyv({ store: redis, namespace: 'scraper' });
      logger.info(`Scraper cache: Redis (${redisUri})`);
    } catch (err) {
      logger.warn(`Failed to initialize Redis store: ${err.message}. Falling back to in-memory cache.`);
      _store = new Keyv({ namespace: 'scraper' });
    }
  } else {
    _store = new Keyv({ namespace: 'scraper' });
    logger.warn('Scraper cache: in-memory (set REDIS_URI for production)');
  }

  _store.on('error', err => logCacheError(err));
  return _store;
}

export async function cacheHas(key) {
  try {
    const store = getStore();
    const hit = await store.get(key);
    return hit !== undefined;
  } catch (err) {
    logCacheError(err);
    return false;
  }
}

export async function cacheWrap(key, loader, ttlSeconds = 3600) {
  let hit;
  try {
    const store = getStore();
    hit = await store.get(key);
  } catch (err) {
    logCacheError(err);
  }

  if (hit !== undefined) return { value: hit, cached: true };

  const value = await loader();
  const isEmpty = Array.isArray(value) && value.length === 0;

  if (!isEmpty) {
    try {
      const store = getStore();
      await store.set(key, value, ttlSeconds * 1000);
    } catch (err) {
      logCacheError(err);
    }
  }

  return { value, cached: false };
}
