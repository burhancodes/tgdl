/**
 * Nyaa provider — uses Nyaa.si RSS/JSON for anime torrents.
 */
import { parseFeed } from '../lib/feedHelper.js';
import { get } from '../lib/httpClient.js';
import { parseTitle, buildSearchQuery } from '../lib/titleHelper.js';
import { logger } from '../lib/logger.js';
import { parseSize } from '../lib/magnetHelper.js';

const BASE = 'https://nyaa.si';

export const id   = 'nyaa';
export const name = 'Nyaa';

export async function scrape(meta) {
  // Nyaa is anime-only
  if (meta.type !== 'series' && meta.type !== 'anime') return [];

  try {
    const query = buildSearchQuery(meta);

    // Nyaa exposes an Atom feed we can parse easily
    const { data } = await get(`${BASE}/?page=rss`, {
      limiterKey: 'nyaa',
      params: {
        q:   query,
        c:   '1_2', // category: Anime – English-translated
        f:   '0',
        s:   'seeders',
        o:   'desc',
      },
      headers: {
        'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36'
      }
    });

    const items = await parseFeed(data);
    const results = [];

    for (const item of items) {
      const title = item.title ? item.title.trim() : '';
      const magnet = item.link || (item['nyaa:infohash'] ? item['nyaa:infohash']['#'] : '');
      const infoHash = (item['nyaa:infohash'] ? item['nyaa:infohash']['#'] : null)
        || extractInfoHash(magnet);

      if (!infoHash || !title) continue;

      const seeders = parseInt(item['nyaa:seeders'] ? item['nyaa:seeders']['#'] : 0, 10) || 0;
      const leechers = parseInt(item['nyaa:leechers'] ? item['nyaa:leechers']['#'] : 0, 10) || 0;
      
      const sizeStr = item['nyaa:size'] ? item['nyaa:size']['#'].trim().replace(/iB/gi, 'B') : '';
      const size = parseSize(sizeStr) || parseInt(sizeStr, 10) || 0;

      results.push({
        infoHash,
        title,
        seeders,
        leechers,
        size,
        provider:  'Nyaa',
        imdbId:    meta.imdbId,
        languages: ['ja'],
        ...parseTitle(title),
      });
    }

    return results;
  } catch (err) {
    logger.warn(`[Nyaa] ${err.message}`);
    return [];
  }
}

function extractInfoHash(magnet = '') {
  const match = magnet.match(/xt=urn:btih:([a-fA-F0-9]{40}|[a-zA-Z2-7]{32})/i);
  return match ? match[1].toLowerCase() : null;
}
