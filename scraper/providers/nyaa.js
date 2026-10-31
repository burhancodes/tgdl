import * as cheerio from 'cheerio';
import { get } from '../lib/httpClient.js';
import { parseTitle, buildSearchQuery } from '../lib/titleHelper.js';
import { extractInfoHash } from '../lib/magnetHelper.js';
import { logger } from '../lib/logger.js';

const BASE = 'https://nyaa.si';

export const id   = 'nyaa';
export const name = 'Nyaa';
export const types = ['series', 'anime'];

export async function scrape(meta) {
  if (meta.type !== 'series' && meta.type !== 'anime') return [];

  try {
    const query = buildSearchQuery(meta);

    const { data } = await get(`${BASE}/?page=rss`, {
      limiterKey: 'nyaa',
      params: {
        q:   query,
        c:   '1_2',
        f:   '0',
        s:   'seeders',
        o:   'desc',
      },
    });

    const $ = cheerio.load(data, { xmlMode: true });
    const results = [];

    $('item').each((_, item) => {
      const $item   = $(item);
      const title   = $item.find('title').text().trim();
      const infoHash = $item.find('nyaa\\:infoHash').text().trim().toLowerCase()
        || extractInfoHash($item.find('link').text());

      if (!infoHash || !title) return;

      const seeders  = parseInt($item.find('nyaa\\:seeders').text().trim(), 10) || 0;
      const leechers = parseInt($item.find('nyaa\\:leechers').text().trim(), 10) || 0;
      const size     = parseInt($item.find('nyaa\\:size').text().trim(), 10) || 0;

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
    });

    return results;
  } catch (err) {
    logger.warn(`[Nyaa] ${err.message}`);
    return [];
  }
}
