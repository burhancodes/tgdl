/**
 * AnimeTosho provider -- uses the Atom/RSS feed for anime torrents.
 * Aggregates results from Nyaa, TokyoTosho, and other anime trackers.
 */
import { parseFeed } from '../lib/feedHelper.js';
import { get } from '../lib/httpClient.js';
import { parseTitle, buildSearchQuery } from '../lib/titleHelper.js';
import { extractInfoHash, parseSize } from '../lib/magnetHelper.js';
import { logger } from '../lib/logger.js';

const FEED_BASE = 'https://feed.animetosho.xyz';

export const id = 'animetosho';
export const name = 'AnimeTosho';

export async function scrape(meta) {
  if (meta.type !== 'series' && meta.type !== 'anime') return [];
  if (!meta?.name) return [];

  try {
    const query = buildSearchQuery(meta);

    const { data } = await get(`${FEED_BASE}/rss2`, {
      limiterKey: 'animetosho',
      params: {
        q: query,
        qx: 1,
        only_tor: 1,
      },
    });

    const items = await parseFeed(data);
    const results = [];

    for (const item of items) {
      const title = item.title ? item.title.trim() : '';
      if (!title) continue;

      const desc = item.description || '';
      const link = item.link || '';

      let infoHash = null;

      const magnetMatch = desc.match(/magnet:\?[^\s"<]+/i)
        || link.match(/magnet:\?[^\s"<]+/i);

      if (magnetMatch) {
        infoHash = extractInfoHash(magnetMatch[0]);
      }

      if (!infoHash) {
        const torrentMatch = desc.match(/\/storage\/torrent\/([a-fA-F0-9]{40})\//i);
        if (torrentMatch) infoHash = torrentMatch[1].toLowerCase();
      }

      if (!infoHash) continue;

      let seeders = 0;
      let leechers = 0;
      const statsMatch = desc.match(/\[(\d+)[^\/]*\/(\d+)/);
      if (statsMatch) {
        seeders  = parseInt(statsMatch[1], 10) || 0;
        leechers = parseInt(statsMatch[2], 10) || 0;
      }

      let size = 0;
      if (item.enclosures && item.enclosures.length > 0) {
        size = parseInt(item.enclosures[0].length, 10) || 0;
      }
      
      if (!size) {
        const sizeMatch = desc.match(/([\d.]+)\s*(GB|MB|TB|KB)/i);
        if (sizeMatch) {
          size = parseSize(`${sizeMatch[1]} ${sizeMatch[2]}`);
        }
      }

      results.push({
        ...parseTitle(title),
        infoHash,
        title,
        seeders,
        leechers,
        size,
        provider:  'AnimeTosho',
        imdbId:    meta.imdbId,
        languages: ['ja'],
      });
    }

    return results;
  } catch (err) {
    logger.warn(`[AnimeTosho] ${err.message}`);
    return [];
  }
}

