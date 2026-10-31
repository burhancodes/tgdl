import { get } from '../lib/httpClient.js';
import { parseTitle, buildSearchQuery } from '../lib/titleHelper.js';
import { tryDomains, PROVIDER_DOMAINS } from '../lib/domainRotation.js';
import { normaliseInfoHash } from '../lib/magnetHelper.js';
import { logger } from '../lib/logger.js';

const DOMAINS = PROVIDER_DOMAINS.thepiratebay;

const MOVIE_CATEGORIES  = new Set(['201', '202', '207', '209', '211']);
const SERIES_CATEGORIES = new Set(['205', '208', '212']);

export const id   = 'thepiratebay';
export const name = 'ThePirateBay';
export const types = ['movie', 'series', 'anime'];

export async function scrape(meta) {
  if (!meta?.name) return [];

  try {
    const query = buildSearchQuery(meta);

    const { data } = await tryDomains(DOMAINS, async (base) => {
      return get(`${base}/q.php`, {
        limiterKey: 'thepiratebay',
        responseType: 'json',
        params: { q: query },
      });
    }, 'TPB', { validate: res => Array.isArray(res?.data) });

    if (data[0]?.id === '0') return [];

    const categories = meta.type === 'movie' ? MOVIE_CATEGORIES : SERIES_CATEGORIES;
    return data
      .filter(t => t.info_hash && categories.has(String(t.category)))
      .map(t => normalise(t, meta));
  } catch (err) {
    logger.warn(`[TPB] ${err.message}`);
    return [];
  }
}

function normalise(t, meta) {
  const parsed = parseTitle(t.name);
  return {
    infoHash:  normaliseInfoHash(t.info_hash),
    title:     t.name,
    seeders:   parseInt(t.seeders ?? '0', 10),
    leechers:  parseInt(t.leechers ?? '0', 10),
    size:      parseInt(t.size ?? '0', 10),
    provider:  'ThePirateBay',
    imdbId:    meta.imdbId,
    ...parsed,
  };
}
