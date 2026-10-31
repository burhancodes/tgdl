import pLimit from 'p-limit';
import * as yts              from './yts.js';
import * as eztv             from './eztv.js';
import * as thepiratebay     from './thepiratebay.js';
import * as torrentgalaxy    from './torrentgalaxy.js';
import * as leetx            from './leetx.js';
import * as kickasstorrents  from './kickasstorrents.js';
import * as nyaa             from './nyaa.js';
import * as animesaturn      from './animesaturn.js';
import * as rutor            from './rutor.js';
import * as rutracker        from './rutracker.js';
import * as limetorrents     from './limetorrents.js';
import * as bitsearch        from './bitsearch.js';
import * as knaben           from './knaben.js';
import * as bt4g             from './bt4g.js';
import * as btdig            from './btdig.js';
import * as glotorrents      from './glotorrents.js';
import * as torlock          from './torlock.js';
import * as torrentdownloads from './torrentdownloads.js';
import * as therarbg         from './therarbg.js';
import * as subsplease       from './subsplease.js';
import * as animetosho       from './animetosho.js';
import * as nekobt           from './nekobt.js';
import * as tokyotosho       from './tokyotosho.js';
import * as torznab          from './torznab.js';
import { logger } from '../lib/logger.js';
import { isSkipped } from '../lib/providerHealth.js';

const ALL_PROVIDERS = [
  yts,
  eztv,
  thepiratebay,
  torrentgalaxy,
  leetx,
  kickasstorrents,
  nyaa,
  animesaturn,
  rutor,
  rutracker,
  limetorrents,
  bitsearch,
  knaben,
  bt4g,
  btdig,
  glotorrents,
  torlock,
  torrentdownloads,
  therarbg,
  subsplease,
  animetosho,
  nekobt,
  tokyotosho,
  torznab,
];

const DISABLED_PROVIDERS = new Set(
  (process.env.SCRAPER_DISABLED_PROVIDERS ?? "")
    .split(",")
    .map(s => s.trim().toLowerCase())
    .filter(Boolean)
);
const ACTIVE_PROVIDERS = ALL_PROVIDERS.filter(p => !DISABLED_PROVIDERS.has(p.id));
if (DISABLED_PROVIDERS.size) {
  logger.info(`Disabled providers: ${[...DISABLED_PROVIDERS].join(", ")}`);
}

const limit = pLimit(Math.max(1, parseInt(process.env.SCRAPER_CONCURRENCY ?? '12', 10) || 12));

const PROVIDER_TIMEOUT_MS = parseInt(process.env.SCRAPER_PROVIDER_TIMEOUT_MS ?? '12000', 10);
const HARD_TIMEOUT_MS     = parseInt(process.env.SCRAPER_HARD_TIMEOUT_MS     ?? '20000', 10);
const EARLY_RETURN_MS     = parseInt(process.env.SCRAPER_EARLY_RETURN_MS     ?? '6000', 10);
const MIN_EARLY_RESULTS   = parseInt(process.env.SCRAPER_MIN_EARLY_RESULTS   ?? '10', 10);

const MAX_ALIAS_QUERIES = parseInt(process.env.SCRAPER_MAX_ALIAS_QUERIES ?? '1', 10);
const MAX_TAG_QUERIES   = parseInt(process.env.SCRAPER_MAX_TAG_QUERIES   ?? '1', 10);

const YEAR_SLACK = 1;
const YEAR_IN_NAME = /\b(?:19|20)\d{2}\b/g;

export async function scrapeAll(type, meta, providerIds = null, context = {}) {
  const requested = ACTIVE_PROVIDERS.filter(p =>
    !providerIds || providerIds.includes(p.id)
  );

  const providers = requested.filter(p => !isSkipped(p.id));
  const skipped = requested.filter(p => isSkipped(p.id)).map(p => p.id);
  if (skipped.length) {
    logger.info(`Skipping providers that are currently failing: ${skipped.join(', ')}`);
  }

  const queries = queryVariants(meta);
  const jobs = providers.flatMap(p =>
    queries.map(({ meta: query, localized }) => ({
      provider: p,
      query,
      localized,
      label: localized ? `${p.name} "${query.name}"` : p.name,
    }))
  );
  if (queries.length > 1) {
    logger.info(`Extra search passes: ${queries.slice(1).map(q => `"${q.meta.name}"`).join(', ')}`);
  }

  const collected = [];
  let matchedCount = 0;
  let resolved = false;
  const startedAt = Date.now();
  let completedCount = 0;
  let pendingLocalized = jobs.filter(j => j.localized).length;

  return new Promise((resolve) => {
    const finalize = () => {
      if (resolved) return;
      resolved = true;
      clearTimeout(earlyTimer);
      clearTimeout(hardTimer);

      logger.info(`Scrape totals: ${collected.length} raw, ${completedCount}/${jobs.length} searches responded`);
      const deduped = deduplicate(collected);
      const matched = filterByContent(deduped, meta);
      if (deduped.length !== matched.length) {
        logger.info(`Content filter: ${deduped.length} -> ${matched.length} (dropped ${deduped.length - matched.length} unrelated)`);
      }
      resolve(matched);
    };

    const earlyTimer = setTimeout(() => {
      if (!resolved && !pendingLocalized && matchedCount >= MIN_EARLY_RESULTS) {
        logger.info(`Early return: ${matchedCount} matching results from ${completedCount}/${jobs.length} searches after ${EARLY_RETURN_MS}ms`);
        finalize();
      }
    }, EARLY_RETURN_MS);

    const hardTimer = setTimeout(() => {
      if (!resolved) {
        logger.info(`Hard timeout: ${collected.length} results from ${completedCount}/${jobs.length} searches after ${HARD_TIMEOUT_MS}ms`);
        finalize();
      }
    }, HARD_TIMEOUT_MS);

    const tasks = jobs.map(({ provider: p, query, label, localized }) =>
      limit(async () => {
        if (resolved) return;
        const start = Date.now();
        try {
          const scrapeType = (!type || type === 'all')
            ? (p.types?.includes('movie') ? 'movie' : (p.types?.[0] ?? 'movie'))
            : type;
          const results = await withTimeout(
            p.scrape({ ...query, ...context, type: scrapeType }),
            PROVIDER_TIMEOUT_MS,
            `${p.name} timed out`
          );
          const kept = localized ? filterByYear(results, meta) : results;
          logger.info(`[${label}] ${kept.length} results in ${Date.now() - start}ms`);
          collected.push(...kept);
          matchedCount += filterByContent(kept, meta).length;
        } catch (err) {
          logger.warn(`[${label}] ${err.message} (${Date.now() - start}ms)`);
        } finally {
          completedCount++;
          if (localized) pendingLocalized--;
          if (completedCount >= jobs.length) finalize();
          else if (!pendingLocalized && Date.now() - startedAt >= EARLY_RETURN_MS && matchedCount >= MIN_EARLY_RESULTS) {
            logger.info(`Early return: ${matchedCount} matching results from ${completedCount}/${jobs.length} searches after ${Date.now() - startedAt}ms`);
            finalize();
          }
        }
      })
    );

    Promise.allSettled(tasks).then(finalize);
  });
}

export function queryVariants(meta) {
  const variants = [{ meta, localized: false }];
  if (!meta?.name) return variants;

  for (const alias of (meta.aliases ?? []).slice(0, Math.max(0, MAX_ALIAS_QUERIES))) {
    variants.push({ meta: { ...meta, name: alias, year: null }, localized: true });
  }
  for (const tag of (meta.searchTags ?? []).slice(0, Math.max(0, MAX_TAG_QUERIES))) {
    variants.push({ meta: { ...meta, name: `${meta.name} ${tag}`, year: null }, localized: true });
  }
  return variants;
}

export function filterByYear(records, meta) {
  if (meta?.type !== 'movie' || !meta.year) return records;
  const year = Number(meta.year);
  if (!Number.isFinite(year)) return records;

  const inTitle = new Set(
    [meta.name, ...(meta.aliases ?? [])].flatMap(n => String(n ?? '').match(YEAR_IN_NAME) ?? [])
  );

  return records.filter(r => {
    const years = (String(r.title ?? '').match(YEAR_IN_NAME) ?? []).filter(y => !inTitle.has(y));
    if (!years.length) return true;
    return years.some(y => Math.abs(Number(y) - year) <= YEAR_SLACK);
  });
}

async function withTimeout(promise, timeoutMs, message) {
  let timeoutId;
  const timeout = new Promise((_, reject) => {
    timeoutId = setTimeout(() => reject(new Error(message)), timeoutMs);
  });

  try {
    return await Promise.race([promise, timeout]);
  } finally {
    clearTimeout(timeoutId);
  }
}

function deduplicate(records) {
  const map = new Map();
  for (const r of records) {
    if (!r.infoHash) continue;
    const existing = map.get(r.infoHash);
    if (!existing || (r.seeders ?? 0) > (existing.seeders ?? 0)) {
      map.set(r.infoHash, r);
    }
  }
  return Array.from(map.values());
}

export function filterByContent(records, meta) {
  if (!meta?.name) return records;

  const matchers = [meta.name, ...(meta.aliases ?? [])]
    .map(name => buildNameMatcher(name, meta.year))
    .filter(Boolean);
  if (!matchers.length) return records;

  return records.filter(r => {
    if (!r.title) return false;
    const norm = normalizeTitle(r.title);

    if (!r.matchedById && !matchers.some(matches => matches(norm))) return false;

    if (meta.type === 'series' && meta.season != null) {
      const s = meta.season;
      const e = meta.episode;
      const hasSeasonEp = new RegExp(
        e != null
          ? `s0*${s}\\s*e0*${e}\\b`
          : `s0*${s}(e\\d|\\b)`,
        'i'
      ).test(r.title);
      const hasLooseEp = e != null && new RegExp(
        `\\b${s}x0*${e}\\b`, 'i'
      ).test(r.title);
      const hasSeasonPack = new RegExp(
        `\\bseason\\s*0*${s}\\b|\\bcomplete\\b.*\\bs0*${s}\\b`,
        'i'
      ).test(r.title);
      if (!hasSeasonEp && !hasLooseEp && !hasSeasonPack) {
        if (!r.matchedById || hasAnySeasonMarker(r.title, e != null)) return false;
      }
    }

    return true;
  });
}

function buildNameMatcher(name, year = null) {
  const words = normalizeTitle(String(name ?? '')).split(/\s+/).filter(w => w.length > 1);
  if (!words.length) return null;

  if (words.length <= 2) {
    const yearRegex = Number.isInteger(Number(year)) ? new RegExp(`\\b${Number(year)}\\b`) : null;
    const phraseRegex = new RegExp(`\\b${words.map(escapeRegex).join('\\s+')}\\b`);
    return norm => {
      const match = phraseRegex.exec(norm);
      if (!match) return false;
      const after = norm.slice(match.index + match[0].length).trim();
      const nextWord = after.match(/^([a-z0-9]+)/);
      if (!nextWord || isTorrentMetadata(nextWord[1])) return true;
      return yearRegex != null && yearRegex.test(norm);
    };
  }

  const wordRegexes = words.map(w => new RegExp(`\\b${escapeRegex(w)}\\b`));
  const needed = Math.max(1, Math.ceil(words.length * 0.5));
  return norm => wordRegexes.filter(re => re.test(norm)).length >= needed;
}

function hasAnySeasonMarker(title, withEpisode) {
  return new RegExp(
    withEpisode
      ? `s\\d+\\s*e\\d+\\b|\\b\\d{1,2}x\\d{1,3}\\b|\\bseason\\s*\\d+\\b|\\bcomplete\\b.*\\bs\\d+\\b`
      : `s\\d+(e\\d|\\b)|\\bseason\\s*\\d+\\b|\\bcomplete\\b.*\\bs\\d+\\b`,
    'i'
  ).test(title);
}

function isTorrentMetadata(word) {
  return /^(s\d|season|episode|ep\d|web|hdtv|bluray|bdrip|dvd|hdrip|cam|x26|h26|hevc|avc|xvid|aac|ac3|dts|multi|dual|repack|proper|internal|extended|unrated|complete|full|part|the)/.test(word)
    || /^\d/.test(word);
}

function escapeRegex(str) {
  return str.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function normalizeTitle(str) {
  return str
    .normalize('NFD')
    .replace(/[\u0300-\u036f]/g, '')
    .toLowerCase()
    .replace(/[^a-z0-9\s]/g, ' ')
    .replace(/\s+/g, ' ')
    .trim();
}

export function listProviders() {
  return ACTIVE_PROVIDERS.map(p => ({ id: p.id, name: p.name, types: [...p.types] }));
}

export function getProvider(id) {
  return ACTIVE_PROVIDERS.find(p => p.id === id);
}

export function classifyProviderIds(requestedIds) {
  const registered = new Set(ALL_PROVIDERS.map(p => p.id));
  const active     = new Set(ACTIVE_PROVIDERS.map(p => p.id));

  return {
    active:   requestedIds.filter(id => active.has(id)),
    disabled: requestedIds.filter(id => registered.has(id) && !active.has(id)),
    unknown:  requestedIds.filter(id => !registered.has(id)),
  };
}
