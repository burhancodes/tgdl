import crypto from 'crypto';
import { readFileSync } from 'node:fs';
import express from 'express';
import { scrapeAll, listProviders, classifyProviderIds } from './providers/index.js';
import { snapshot as providerSnapshot } from './lib/providerHealth.js';
import { getMetadata } from './lib/cinemeta.js';
import { getLocalizedTitles, releaseTagsFor } from './lib/localizedTitles.js';
import { cacheWrap } from './lib/cache.js';
import { startCronJobs } from './lib/cron.js';
import { runPrewarm, getPrewarmStatus } from './lib/prewarm.js';
import { logger } from './lib/logger.js';
import { rpcMiddleware } from './rpcHandler.js';

const app = express();
const PORT = process.env.PORT || 8080;
const deploymentRevision = (() => {
  try {
    return readFileSync('.revision', 'utf8').trim() || undefined;
  } catch { return undefined; }
})();

const TTL_STREAMS = parseInt(process.env.CACHE_TTL_STREAMS ?? '3600', 10);

app.use(express.json());
app.set('trust proxy', 1);

app.post('/rpc', rpcMiddleware);

app.get('/health', (_req, res) => {
  res.json({ status: 'ok', service: 'magnetio-scraper', version: '1.2.0', revision: deploymentRevision });
});

app.get('/providers', (_req, res) => {
  res.json({ providers: listProviders() });
});

app.get('/providers/health', (_req, res) => {
  const providers = providerSnapshot();
  const degraded = providers.filter(p => !p.healthy);
  res.status(degraded.length ? 503 : 200).json({
    status: degraded.length ? 'degraded' : 'ok',
    degraded: degraded.map(p => ({ id: p.id, kind: p.lastErrorKind, error: p.lastError })),
    providers,
  });
});

app.get('/streams/:type/:id', async (req, res) => {
  const { type, id } = req.params;
  const providerIds = req.query.providers
    ? req.query.providers.split(',').map(s => s.trim()).filter(Boolean)
    : null;

  if (providerIds) {
    const { active, disabled, unknown } = classifyProviderIds(providerIds);

    if (disabled.length) {
      logger.info(`Providers disabled on this host, skipping: ${disabled.join(', ')}`);
    }
    if (unknown.length && active.length) {
      logger.warn(`Ignoring unknown provider ids: ${unknown.join(', ')}`);
    }

    if (!active.length) {
      const error = unknown.length
        ? `Unknown provider ids: ${unknown.join(', ')}`
        : `Every requested provider is disabled on this host: ${disabled.join(', ')}`;
      return res.status(400).json({
        error,
        ...(unknown.length ? { unknownProviders: unknown } : {}),
        ...(disabled.length ? { disabledProviders: disabled } : {}),
        knownProviders: listProviders().map(p => p.id).sort(),
      });
    }
  }

  const torznabUrl = req.query.torznabUrl || null;
  const torznabApiKey = req.query.torznabApiKey || null;
  const context = { torznabUrl, torznabApiKey };

  const languages = req.query.languages
    ? String(req.query.languages).split(',').map(l => l.trim().toLowerCase()).filter(Boolean)
    : [];
  const tmdbApiKey = req.query.tmdbApiKey ? String(req.query.tmdbApiKey) : (process.env.TMDB_API_KEY || null);

  if (!['movie', 'series', 'anime'].includes(type)) {
    return res.status(400).json({ error: 'Invalid type. Use movie, series, or anime.' });
  }

  const torznabSuffix = torznabUrl
    ? `:tz-${crypto.createHash('sha256').update(torznabUrl + (torznabApiKey || '')).digest('hex').slice(0, 12)}`
    : '';
  const languageSuffix = languages.length ? `:lang-${[...languages].sort().join('+')}` : '';
  const aliasSuffix = languages.length && tmdbApiKey ? ':loc' : '';
  const cacheKey = `streams:${type}:${id}:${providerIds?.join(',') ?? 'all'}${torznabSuffix}${languageSuffix}${aliasSuffix}`;

  try {
    const { value: streams, cached } = await cacheWrap(cacheKey, async () => {
      const meta = await getMetadata(type === 'anime' ? 'series' : type, id);
      if (!meta) {
        logger.warn(`No metadata for ${type}/${id}, skipping`);
        return [];
      }

      const label = meta.year ? `"${meta.name}" (${meta.year})` : `"${meta.name}"`;
      logger.info(`Scraping ${label} [${type}] from ${providerIds?.join(',') ?? 'all providers'}`);

      const enriched = {
        ...meta,
        type,
        aliases: await getLocalizedTitles(meta, languages, tmdbApiKey),
        searchTags: releaseTagsFor(languages),
      };

      return scrapeAll(type, enriched, providerIds, context);
    }, TTL_STREAMS);

    res.json({ streams, cached });
  } catch (err) {
    logger.error(`Error scraping ${type}/${id}: ${err.message}`);
    res.status(500).json({ error: 'Internal server error' });
  }
});

app.post('/prewarm', (_req, res) => {
  const { running } = getPrewarmStatus();
  if (running) return res.status(409).json({ error: 'Prewarm already running' });
  runPrewarm();
  res.json({ status: 'started' });
});

app.get('/prewarm', (_req, res) => {
  res.json(getPrewarmStatus());
});

app.use((_req, res) => res.status(404).json({ error: 'Not found' }));

const server = app.listen(PORT, () => {
  logger.info(`Magnetio scraper running on port ${PORT}`);
  startCronJobs();
});

export { app, server };
