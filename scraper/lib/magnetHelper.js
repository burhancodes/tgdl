export function normaliseInfoHash(value) {
  if (typeof value !== 'string') return null;
  const raw = value.trim();
  if (/^[a-fA-F0-9]{40}$/.test(raw)) return raw.toLowerCase();
  if (/^[a-zA-Z2-7]{32}$/.test(raw)) return base32ToHex(raw);
  return null;
}

export function extractInfoHash(magnet = '') {
  const match = magnet.match(/xt=urn:btih:([a-fA-F0-9]{40}|[a-zA-Z2-7]{32})/i);
  if (!match) return null;
  return normaliseInfoHash(match[1]);
}

export function base32ToHex(base32) {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
  let bits = '';
  const upper = base32.toUpperCase();
  for (let i = 0; i < upper.length; i++) {
    const val = alphabet.indexOf(upper[i]);
    if (val === -1) return null;
    bits += val.toString(2).padStart(5, '0');
  }
  let hex = '';
  for (let i = 0; i + 4 <= bits.length; i += 4) {
    hex += parseInt(bits.substr(i, 4), 2).toString(16);
  }
  return hex.toLowerCase();
}

export function parseSize(sizeStr = '') {
  if (typeof sizeStr === 'number') return sizeStr;
  const str = String(sizeStr).trim();
  const m = str.match(/^([\d.,]+)\s*([a-zA-Z]+)?$/);
  if (!m) return 0;
  const val = parseFloat(m[1].replace(/,/g, ''));
  if (isNaN(val)) return 0;
  const units = { b: 1, kb: 1024, mb: 1024 ** 2, gb: 1024 ** 3, tb: 1024 ** 4 };
  return Math.round(val * (units[m[2].toLowerCase()] ?? 1));
}

export const DEFAULT_TRACKERS = [
  'udp://tracker.opentrackr.org:1337/announce',
  'udp://open.stealth.si:80/announce',
  'udp://tracker.torrent.eu.org:451/announce',
  'udp://tracker.bittorrent.eu:451/announce',
  'udp://explodie.org:6969/announce',
  'udp://open.demonii.com:1337/announce',
  'udp://tracker.openbittorrent.com:80',
];

export function buildMagnetUrl({ infoHash, title, trackers }) {
  if (!infoHash) return null;
  const hash = infoHash.toLowerCase();
  let url = `magnet:?xt=urn:btih:${hash}`;
  if (title) {
    url += `&dn=${encodeURIComponent(title)}`;
  }
  const trList = (Array.isArray(trackers) && trackers.length > 0) ? trackers : DEFAULT_TRACKERS;
  for (const tr of trList) {
    url += `&tr=${encodeURIComponent(tr)}`;
  }
  return url;
}
