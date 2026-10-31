import axios from 'axios';
import { cacheWrap } from './cache.js';
import { logger } from './logger.js';

const TMDB_BASE = 'https://api.themoviedb.org/3';
const TIMEOUT   = 6000;

const WIKIDATA_SPARQL = 'https://query.wikidata.org/sparql';
const WIKIDATA_UA = 'Magnetio/1.0 (https://github.com/peterdsp/Magnetio)';
const WIKIDATA_TIMEOUT = 8000;

const TTL = 7 * 24 * 3600;

const MAX_LANGUAGES = parseInt(process.env.SCRAPER_MAX_ALIAS_LANGUAGES ?? '3', 10);

const RELEASE_TAGS = {
  it: 'ITA',
  fr: 'FRENCH',
  es: 'SPANISH',
  pt: 'PORTUGUESE',
  de: 'GERMAN',
  ru: 'RUSSIAN',
  el: 'GREEK',
  tr: 'TURKISH',
  pl: 'POLISH',
  nl: 'DUTCH',
  cs: 'CZECH',
  hu: 'HUNGARIAN',
  ro: 'ROMANIAN',
  hi: 'HINDI',
  ta: 'TAMIL',
  te: 'TELUGU',
  ar: 'ARABIC',
  ko: 'KOREAN',
  ja: 'JAPANESE',
  zh: 'CHINESE',
  sq: 'ALBANIAN',
};

function targetLanguages(languages) {
  return [...new Set(
    (languages ?? [])
      .map(l => String(l).trim().toLowerCase())
      .filter(l => l && l !== 'en' && l !== 'multi')
  )].slice(0, Math.max(0, MAX_LANGUAGES));
}

export function releaseTagsFor(languages) {
  return targetLanguages(languages)
    .map(l => RELEASE_TAGS[l])
    .filter(Boolean);
}

async function resolveTmdb(imdbId, type, apiKey) {
  const { value } = await cacheWrap(`tmdb-find:${type}:${imdbId}`, async () => {
    const { data } = await axios.get(`${TMDB_BASE}/find/${imdbId}`, {
      params: { api_key: apiKey, external_source: 'imdb_id' },
      timeout: TIMEOUT,
    });
    const movie = data?.movie_results?.[0];
    const tv    = data?.tv_results?.[0];
    const first = type === 'movie' ? (movie ?? tv) : (tv ?? movie);
    if (!first) return null;
    return { id: first.id, endpoint: first === tv ? 'tv' : 'movie' };
  }, TTL);

  return value;
}

async function fetchTitle({ id, endpoint }, language, apiKey) {
  const { value } = await cacheWrap(`tmdb-title:${endpoint}:${id}:${language}`, async () => {
    const { data } = await axios.get(`${TMDB_BASE}/${endpoint}/${id}`, {
      params: { api_key: apiKey, language },
      timeout: TIMEOUT,
    });
    return data?.title ?? data?.name ?? null;
  }, TTL);

  return value;
}

async function fromWikidata(imdbId, languages) {
  if (!/^tt\d{5,12}$/.test(imdbId)) return [];

  const { value } = await cacheWrap(
    `wikidata-titles:${imdbId}:${languages.join('+')}`,
    async () => {
      const langList = languages.map(l => `"${l}"`).join(',');
      const query = `SELECT ?label WHERE { ?film wdt:P345 "${imdbId}". ` +
                    `?film rdfs:label ?label. FILTER(LANG(?label) IN (${langList})) }`;

      const { data } = await axios.get(WIKIDATA_SPARQL, {
        params: { query, format: 'json' },
        timeout: WIKIDATA_TIMEOUT,
        headers: {
          'User-Agent': WIKIDATA_UA,
          Accept: 'application/sparql-results+json',
        },
      });

      const rows = data?.results?.bindings ?? [];
      return rows.map(r => r?.label?.value).filter(Boolean);
    },
    TTL
  );

  return value ?? [];
}

async function fromTmdb(meta, languages, key) {
  const tmdb = await resolveTmdb(meta.imdbId, meta.type, key);
  if (!tmdb) {
    logger.debug(`[Localized] no TMDB match for ${meta.imdbId}`);
    return [];
  }

  const titles = await Promise.all(
    languages.map(lang =>
      fetchTitle(tmdb, lang, key).catch(err => {
        logger.debug(`[Localized] ${meta.imdbId} ${lang}: ${err.message}`);
        return null;
      })
    )
  );
  return titles.filter(Boolean);
}

export async function getLocalizedTitles(meta, languages, apiKey = null) {
  const key = apiKey || process.env.TMDB_API_KEY || null;
  const targets = targetLanguages(languages);
  if (!targets.length || !meta?.imdbId || !meta?.name) return [];

  try {
    let titles = [];
    let source = 'wikidata';
    if (key) {
      titles = await fromTmdb(meta, targets, key);
      if (titles.length) source = 'tmdb';
    }
    if (!titles.length) {
      titles = await fromWikidata(meta.imdbId, targets);
    }

    const original = compare(meta.name);
    const aliases = [];
    for (const title of titles) {
      if (!title) continue;
      const normalized = compare(title);
      if (!normalized || normalized === original) continue;
      if (aliases.some(a => compare(a) === normalized)) continue;
      aliases.push(title);
    }

    if (aliases.length) {
      logger.info(`[Localized:${source}] "${meta.name}" also known as ${aliases.map(a => `"${a}"`).join(', ')}`);
    }
    return aliases;
  } catch (err) {
    logger.warn(`[Localized] ${meta.imdbId}: ${err.message}`);
    return [];
  }
}

function compare(title) {
  return String(title)
    .normalize('NFD')
    .replace(/[̀-ͯ]/g, '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, ' ')
    .trim();
}

