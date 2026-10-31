import { post } from '../lib/httpClient.js';
import { parseTitle, buildSearchQuery } from '../lib/titleHelper.js';
import { extractInfoHash, normaliseInfoHash } from '../lib/magnetHelper.js';
import { logger } from '../lib/logger.js';

const API = 'https://api.knaben.org/v1';

export const id   = 'knaben';
export const name = 'Knaben';
export const types = ['movie', 'series', 'anime'];

const VIDEO_CATEGORY_PREFIXES = [2, 5, 1];

export async function scrape(meta) {
  if (!meta?.name) return [];

  try {
    const { data } = await post(API, {
      limiterKey: id,
      responseType: 'json',
      data: {
        query:           buildSearchQuery(meta),
        search_type:     '100%',
        size:            50,
        order_by:        'seeders',
        order_direction: 'desc',
        hide_unsafe:     true,
      },
    });

    const hits = Array.isArray(data?.hits) ? data.hits : [];
    return hits.flatMap(hit => normalise(hit, meta) ?? []);
  } catch (err) {
    logger.warn(`[Knaben] ${err.message}`);
    return [];
  }
}

export function isVideo(hit) {
  const ids = Array.isArray(hit.categoryId) ? hit.categoryId : [];
  if (!ids.length) return true;
  return ids.some(c => VIDEO_CATEGORY_PREFIXES.includes(Math.floor(c / 1000000)));
}

export function normalise(hit, meta) {
  if (!isVideo(hit)) return null;

  const title = (hit.title ?? '').trim();
  if (!title) return null;

  const infoHash = extractInfoHash(hit.magnetUrl ?? '') ?? normaliseInfoHash(hit.hash);
  if (!infoHash) return null;

  const seeders  = hit.liveSeeders || hit.seeders || 0;
  const leechers = hit.livePeers   || hit.peers   || 0;

  return {
    ...parseTitle(title),
    infoHash,
    title,
    seeders,
    leechers,
    size:     Number(hit.bytes) || 0,
    provider: name,
    imdbId:   meta.imdbId,
  };
}

