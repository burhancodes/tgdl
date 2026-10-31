import { SocksProxyAgent } from 'socks-proxy-agent';
import { HttpsProxyAgent } from 'https-proxy-agent';
import { logger } from './logger.js';

const SOCKS_SCHEMES = new Set(['socks:', 'socks4:', 'socks4a:', 'socks5:', 'socks5h:']);
const HTTP_SCHEMES  = new Set(['http:', 'https:']);

let configured = null;

function config() {
  if (configured) return configured;

  const url = (process.env.SCRAPER_PROXY_URL ?? '').trim();
  const only = (process.env.SCRAPER_PROXY_PROVIDERS ?? '')
    .split(',')
    .map(s => s.trim().toLowerCase())
    .filter(Boolean);

  configured = { url, only: new Set(only), agents: null, warned: false };
  if (url) {
    logger.info(
      `Outbound proxy enabled for ${only.length ? only.join(', ') : 'all providers'}`
    );
  }
  return configured;
}

export function proxyAppliesTo(limiterKey) {
  const { url, only } = config();
  if (!url) return false;
  if (!only.size) return true;
  return only.has(String(limiterKey ?? '').toLowerCase());
}

export function proxyAgents() {
  const state = config();
  if (!state.url) return null;
  if (state.agents) return state.agents;

  let scheme;
  try {
    scheme = new URL(state.url).protocol;
  } catch {
    if (!state.warned) {
      logger.error(`SCRAPER_PROXY_URL is not a valid URL, ignoring it: ${state.url}`);
      state.warned = true;
    }
    return null;
  }

  try {
    if (SOCKS_SCHEMES.has(scheme)) {
      const agent = new SocksProxyAgent(state.url);
      state.agents = { httpAgent: agent, httpsAgent: agent };
    } else if (HTTP_SCHEMES.has(scheme)) {
      const agent = new HttpsProxyAgent(state.url);
      state.agents = { httpAgent: agent, httpsAgent: agent };
    } else {
      if (!state.warned) {
        logger.error(`SCRAPER_PROXY_URL scheme "${scheme}" is not supported, ignoring it`);
        state.warned = true;
      }
      return null;
    }
  } catch (err) {
    if (!state.warned) {
      logger.error(`SCRAPER_PROXY_URL could not be used, ignoring it: ${err.message}`);
      state.warned = true;
    }
    return null;
  }

  return state.agents;
}

export function resetProxy() {
  configured = null;
}

