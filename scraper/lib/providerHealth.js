import { logger } from './logger.js';

const ALERT_AFTER = 3;
const COOLDOWN_MS = parseInt(process.env.SCRAPER_PROVIDER_COOLDOWN_MS ?? '300000', 10);

const health = new Map();

function entry(id) {
  if (!health.has(id)) {
    health.set(id, {
      ok: 0,
      failed: 0,
      consecutiveFailures: 0,
      kinds: {},
      lastOkAt: null,
      lastFailureAt: null,
      lastError: null,
      lastErrorKind: null,
    });
  }
  return health.get(id);
}

export function classifyError(err) {
  const code = err?.code;
  if (code === 'ENOTFOUND') return 'dns';
  if (code === 'EAI_AGAIN') return 'dns-temporary';
  if (code === 'ECONNREFUSED' || code === 'ECONNRESET' || code === 'EHOSTUNREACH') return 'connection';
  if (code === 'ETIMEDOUT' || code === 'ECONNABORTED' || /timed\s?out|timeout/i.test(err?.message ?? '')) return 'timeout';
  if (err?.response?.status) return 'http';
  return 'other';
}

export function recordSuccess(id) {
  if (!id || id === 'default') return;
  const e = entry(id);
  e.ok++;
  e.consecutiveFailures = 0;
  e.lastOkAt = new Date().toISOString();
}

export function recordFailure(id, err) {
  if (!id || id === 'default') return;
  const e = entry(id);
  const kind = classifyError(err);

  e.failed++;
  e.consecutiveFailures++;
  e.kinds[kind] = (e.kinds[kind] ?? 0) + 1;
  e.lastFailureAt = new Date().toISOString();
  e.lastError = err?.message ?? String(err);
  e.lastErrorKind = kind;

  if (e.consecutiveFailures === ALERT_AFTER) {
    const mix = Object.entries(e.kinds).map(([k, n]) => `${k}=${n}`).join(' ');
    logger.error(
      `[${id}] ${ALERT_AFTER} consecutive request failures [${mix}], last: ${e.lastError}`
    );
  }
}

export function isSkipped(id) {
  const e = health.get(id);
  if (!e || e.consecutiveFailures < ALERT_AFTER || !e.lastFailureAt) return false;
  return Date.now() - Date.parse(e.lastFailureAt) < COOLDOWN_MS;
}

export function snapshot() {
  return [...health.entries()]
    .map(([id, e]) => ({
      id,
      ...e,
      healthy: e.consecutiveFailures < ALERT_AFTER,
      skipped: isSkipped(id),
    }))
    .sort((a, b) => b.consecutiveFailures - a.consecutiveFailures || a.id.localeCompare(b.id));
}

export function reset() {
  health.clear();
}

