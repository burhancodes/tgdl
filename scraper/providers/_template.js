import * as cheerio from 'cheerio';
import { get } from '../lib/httpClient.js';
import { parseTitle, buildSearchQuery } from '../lib/titleHelper.js';
import { extractInfoHash, parseSize } from '../lib/magnetHelper.js';
import { logger } from '../lib/logger.js';

const BASE = 'https://example.com';

export const id   = 'template';
export const name = 'Template';
export const types = ['movie', 'series', 'anime'];

export async function scrape(meta) {
  if (!meta?.name) return [];

  try {
    const query = buildSearchQuery(meta);

    const { data } = await get(`${BASE}/search`, {
      limiterKey: id,
      params: { q: query },
    });

    const results = [];

    const $ = cheerio.load(data, { xmlMode: true });
    $('item').each((_, item) => {
      const $item = $(item);

      const title = $item.find('title').first().text().trim();
      if (!title) return;

      const magnet = $item.find('link').first().text().trim();
      const infoHash = extractInfoHash(magnet);
      if (!infoHash || results.some(r => r.infoHash === infoHash)) return;

      results.push({
        infoHash,
        title,
        seeders:  0,
        leechers: 0,
        size:     parseSize($item.find('size').first().text()),
        provider: name,
        imdbId:   meta.imdbId,
        ...parseTitle(title),
      });
    });

    return results;
  } catch (err) {
    logger.warn(`[Template] ${err.message}`);
    return [];
  }
}

