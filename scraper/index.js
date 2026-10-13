import 'dotenv/config';
import crypto from 'crypto';
import { readFileSync } from 'node:fs';
import express from 'express';
import { scrapeAll, listProviders } from './providers/index.js';
import { getMetadata } from './lib/cinemeta.js';
import { cacheWrap } from './lib/cache.js';
import { startCronJobs } from './lib/cron.js';
import { runPrewarm, getPrewarmStatus } from './lib/prewarm.js';
import { logger } from './lib/logger.js';
import { rpcMiddleware } from './rpcHandler.js';

const app = express();
const PORT = process.env.PORT || 8080;
const deploymentRevision = (() => {
  try {
    const value = readFileSync(new URL('./.deployment-revision', import.meta.url), 'utf8').trim();
    return /^[a-f0-9]{40}$/.test(value) ? value : undefined;
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

app.get('/streams/:type/:id', async (req, res) => {
  const { type, id } = req.params;
  const providerIds = req.query.providers
    ? req.query.providers.split(',').map(s => s.trim()).filter(Boolean)
    : null;

  const torznabUrl = req.query.torznabUrl || null;
  const torznabApiKey = req.query.torznabApiKey || null;
  const context = { torznabUrl, torznabApiKey };

  if (!['movie', 'series', 'anime'].includes(type)) {
    return res.status(400).json({ error: 'Invalid type. Use movie, series, or anime.' });
  }

  const torznabSuffix = torznabUrl
    ? `:tz-${crypto.createHash('sha256').update(torznabUrl + (torznabApiKey || '')).digest('hex').slice(0, 12)}`
    : '';
  const cacheKey = `streams:${type}:${id}:${providerIds?.join(',') ?? 'all'}${torznabSuffix}`;

  try {
    const { value: streams, cached } = await cacheWrap(cacheKey, async () => {
      const meta = await getMetadata(type === 'anime' ? 'series' : type, id);
      if (!meta) {
        logger.warn(`No metadata for ${type}/${id}, skipping`);
        return [];
      }

      const label = meta.year ? `"${meta.name}" (${meta.year})` : `"${meta.name}"`;
      logger.info(`Scraping ${label} [${type}] from ${providerIds?.join(',') ?? 'all providers'}`);

      return scrapeAll(type, meta, providerIds, { ...context, strict: true }, true);
    }, TTL_STREAMS);

    res.json({ streams, cached });
  } catch (err) {
    logger.error(`Streams error [${id}]: ${err.message}`);
    res.status(500).json({ error: err.message, streams: [] });
  }
});

app.post('/prewarm', (_req, res) => {
  const { running } = getPrewarmStatus();
  if (running) return res.status(409).json({ error: 'Prewarm already running' });
  runPrewarm().catch(err => logger.error(`[Prewarm] ${err.message}`));
  res.status(202).json({ message: 'Prewarm started' });
});

app.get('/prewarm/status', (_req, res) => {
  res.json(getPrewarmStatus());
});

app.use((_req, res) => res.status(404).json({ error: 'Not found' }));

const server = app.listen(PORT, () => {
  logger.info(`Magnetio scraper running on port ${PORT}`);
  startCronJobs();
});

const shutdown = () => {
  logger.info('Shutting down…');
  server.close(() => process.exit(0));
  setTimeout(() => process.exit(1), 10_000);
};
process.on('SIGTERM', shutdown);
process.on('SIGINT', shutdown);

export default app;
