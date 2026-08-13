/**
 * nekoBT provider -- uses the public Torznab-compatible RSS API.
 * Anime-focused tracker with fansub content. No auth required.
 */
import { parseFeed } from '../lib/feedHelper.js';
import { get } from '../lib/httpClient.js';
import { parseTitle, buildSearchQuery } from '../lib/titleHelper.js';
import { extractInfoHash } from '../lib/magnetHelper.js';
import { logger } from '../lib/logger.js';

const BASE = 'https://nekobt.to';

export const id   = 'nekobt';
export const name = 'nekoBT';

export async function scrape(meta) {
  if (meta.type !== 'series' && meta.type !== 'anime') return [];
  if (!meta?.name) return [];

  try {
    const query = buildSearchQuery(meta);

    const { data } = await get(`${BASE}/api/torznab/api`, {
      limiterKey: 'nekobt',
      params: {
        t:   'search',
        q:   query,
        cat: '5070',  // anime category
      },
    });

    const items = await parseFeed(data);
    const results = [];

    for (const item of items) {
      const title = item.title ? item.title.trim() : '';
      if (!title) continue;

      let infoHash = getAttr(item, 'infohash');
      if (!infoHash) {
        const magnetUrl = getAttr(item, 'magneturl') || (item.link ? item.link.trim() : '');
        if (magnetUrl) infoHash = extractInfoHash(magnetUrl);
      }
      if (!infoHash) continue;

      const seeders = parseInt(getAttr(item, 'seeders') || '0', 10);
      const leechers = parseInt(getAttr(item, 'peers') || '0', 10) - seeders;
      const size = parseInt(getAttr(item, 'size') || (item.size ? item.size : '0'), 10);

      results.push({
        ...parseTitle(title),
        infoHash: infoHash.toLowerCase(),
        title,
        seeders:   seeders || 0,
        leechers:  Math.max(0, leechers) || 0,
        size:      size || 0,
        provider:  'nekoBT',
        imdbId:    meta.imdbId,
        languages: ['ja'],
      });
    }

    return results;
  } catch (err) {
    logger.warn(`[nekoBT] ${err.message}`);
    return [];
  }
}

/**
 * Read a torznab:attr value by name from a feedparser item.
 */
function getAttr(item, name) {
  const attrs = item['torznab:attr'];
  if (!attrs) return null;
  const attrArray = Array.isArray(attrs) ? attrs : [attrs];
  for (const attr of attrArray) {
    if (attr['@'] && attr['@'].name === name) {
      return attr['@'].value;
    }
  }
  return null;
}

