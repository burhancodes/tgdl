import { scrapeAll, listProviders } from './providers/index.js';
import { buildMagnetUrl } from './lib/magnetHelper.js';
import { logger } from './lib/logger.js';
import { scrapeXenForo, resolveSingleMedia } from './xenforo/index.js';

function makeError(id, code, message, data) {
  return {
    jsonrpc: '2.0',
    id: id ?? null,
    error: {
      code,
      message,
      ...(data !== undefined ? { data } : {}),
    },
  };
}

function makeResult(id, result) {
  return {
    jsonrpc: '2.0',
    id: id ?? null,
    result,
  };
}

function checkAuth(req, paramsSecret) {
  const expectedSecret = process.env.RPC_SHARED_SECRET;
  if (!expectedSecret) return true;

  const authHeader = req.headers?.authorization;
  if (authHeader && authHeader.startsWith('Bearer ')) {
    const token = authHeader.slice(7).trim();
    if (token === expectedSecret) return true;
  }

  if (paramsSecret && paramsSecret === expectedSecret) {
    return true;
  }

  return false;
}

async function handleSingleRpc(req, reqObj) {
  if (!reqObj || typeof reqObj !== 'object' || Array.isArray(reqObj)) {
    return makeError(null, -32600, 'Invalid Request');
  }

  const { jsonrpc, method, params, id } = reqObj;

  if (jsonrpc !== '2.0') {
    return makeError(id, -32600, 'Invalid Request: jsonrpc must be "2.0"');
  }

  if (typeof method !== 'string') {
    return makeError(id, -32600, 'Invalid Request: method string is required');
  }

  const paramsObj = (params && typeof params === 'object' && !Array.isArray(params)) ? params : {};
  const paramsSecret = paramsObj.secret;

  if (!checkAuth(req, paramsSecret)) {
    return makeError(id, -32001, 'Unauthorized');
  }

  try {
    switch (method) {
      case 'torrent.search': {
        const query = paramsObj.query || (Array.isArray(params) ? params[0] : null);
        if (!query || typeof query !== 'string') {
          return makeError(id, -32602, 'Invalid params: query string is required');
        }

        const type = paramsObj.type || 'movie';
        const year = paramsObj.year ? parseInt(paramsObj.year, 10) : undefined;
        const season = paramsObj.season ? parseInt(paramsObj.season, 10) : undefined;
        const episode = paramsObj.episode ? parseInt(paramsObj.episode, 10) : undefined;
        const providers = Array.isArray(paramsObj.providers) ? paramsObj.providers : null;
        const limit = paramsObj.limit ? parseInt(paramsObj.limit, 10) : null;
        const strict = paramsObj.strict !== undefined ? Boolean(paramsObj.strict) : false;

        const meta = { name: query, year, season, episode, type };
        const records = await scrapeAll(type, meta, providers, { strict }, strict);

        let mapped = records.map(r => {
          const magnet = r.magnet || buildMagnetUrl({ infoHash: r.infoHash, title: r.title, trackers: r.trackers });
          return {
            title: r.title || 'Unknown',
            infoHash: r.infoHash || null,
            seeders: parseInt(r.seeders ?? 0, 10),
            leechers: parseInt(r.leechers ?? 0, 10),
            size: parseInt(r.size ?? 0, 10),
            provider: r.provider || 'unknown',
            quality: r.quality || null,
            codec: r.codec || null,
            source: r.source || null,
            languages: Array.isArray(r.languages) ? r.languages : [],
            magnet: magnet || null,
          };
        });

        if (limit && limit > 0) {
          mapped = mapped.slice(0, limit);
        }

        return makeResult(id, { torrents: mapped, count: mapped.length });
      }

      case 'torrent.providers': {
        return makeResult(id, { providers: listProviders() });
      }

      case 'torrent.health': {
        return makeResult(id, { status: 'ok', service: 'magnetio-scraper', version: '1.1.5' });
      }

      case 'xenforo.scrape':
      case 'forum.scrape': {
        const url = paramsObj.url || (Array.isArray(params) ? params[0] : null);
        if (!url || typeof url !== 'string') {
          return makeError(id, -32602, 'Invalid params: url string is required');
        }

        const userId = paramsObj.user_id || paramsObj.userId || null;
        const cookiesTxt = paramsObj.cookies || paramsObj.cookiesTxt || null;
        const userAgent = paramsObj.userAgent || paramsObj.user_agent || null;
        const passwords = Array.isArray(paramsObj.passwords) ? paramsObj.passwords : [];
        const maxPages = paramsObj.maxPages ? parseInt(paramsObj.maxPages, 10) : 1;
        const enabledHosts = Array.isArray(paramsObj.enabledHosts) ? paramsObj.enabledHosts : null;
        const concurrency = paramsObj.concurrency ? parseInt(paramsObj.concurrency, 10) : 8;

        const result = await scrapeXenForo({
          url,
          userId,
          cookiesTxt,
          userAgent,
          passwords,
          maxPages,
          enabledHosts,
          concurrency,
        });

        return makeResult(id, result);
      }

      case 'xenforo.resolve':
      case 'forum.resolve': {
        const url = paramsObj.url || (Array.isArray(params) ? params[0] : null);
        if (!url || typeof url !== 'string') {
          return makeError(id, -32602, 'Invalid params: url string is required');
        }

        const userId = paramsObj.user_id || paramsObj.userId || null;
        const cookiesTxt = paramsObj.cookies || paramsObj.cookiesTxt || null;
        const userAgent = paramsObj.userAgent || paramsObj.user_agent || null;
        const passwords = Array.isArray(paramsObj.passwords) ? paramsObj.passwords : [];

        const items = await resolveSingleMedia({
          url,
          userId,
          cookiesTxt,
          userAgent,
          passwords,
        });

        return makeResult(id, { url, items, count: items.length });
      }

      default:
        return makeError(id, -32601, `Method not found: ${method}`);
    }
  } catch (err) {
    logger.error(`RPC error [${method}]: ${err.message}`);
    return makeError(id, -32603, 'Internal error', err.message);
  }
}

export async function rpcMiddleware(req, res) {
  const body = req.body;

  if (!body) {
    return res.status(200).json(makeError(null, -32700, 'Parse error'));
  }

  if (Array.isArray(body)) {
    if (body.length === 0) {
      return res.status(200).json(makeError(null, -32600, 'Invalid Request'));
    }
    const responses = await Promise.all(body.map(item => handleSingleRpc(req, item)));
    return res.status(200).json(responses);
  }

  const response = await handleSingleRpc(req, body);
  return res.status(200).json(response);
}
