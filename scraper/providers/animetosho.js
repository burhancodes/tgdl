import { get } from '../lib/httpClient.js';
import { parseTitle, buildSearchQuery } from '../lib/titleHelper.js';
import { extractInfoHash, normaliseInfoHash } from '../lib/magnetHelper.js';
import { logger } from '../lib/logger.js';

const BASE = 'https://animetosho.org';

export const id   = 'animetosho';
export const name = 'AnimeTosho';
export const types = ['series', 'anime'];

export async function scrape(meta) {
  if (meta.type !== 'series' && meta.type !== 'anime') return [];
  if (!meta?.name) return [];

  try {
    const { data } = await get(`${BASE}/feed/json`, {
      limiterKey: id,
      responseType: 'json',
      params: {
        q:        buildSearchQuery(meta),
        qx:       1,
        only_tor: 1,
      },
    });

    const entries = Array.isArray(data) ? data : [];
    return entries.flatMap(entry => normalise(entry, meta) ?? []);
  } catch (err) {
    logger.warn(`[AnimeTosho] ${err.message}`);
    return [];
  }
}

export function normalise(entry, meta = {}) {
  const title = (entry.title ?? entry.torrent_name ?? '').trim();
  if (!title) return null;

  const infoHash = normaliseInfoHash(entry.info_hash)
    ?? extractInfoHash(entry.magnet_uri ?? '');
  if (!infoHash) return null;

  return {
    ...parseTitle(title),
    infoHash,
    title,
    seeders:   Math.max(0, Number(entry.seeders)  || 0),
    leechers:  Math.max(0, Number(entry.leechers) || 0),
    size:      Number(entry.total_size) || 0,
    provider:  name,
    imdbId:    meta.imdbId,
    languages: ['ja'],
  };
}
