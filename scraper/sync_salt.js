#!/usr/bin/env node
import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import { fileURLToPath } from 'node:url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

const DEFAULT_UA = 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.7922.71 Safari/537.36';
const WT_URLS = [
  'https://gofile.io/dist/js/wt.obf.js',
  'https://gofile.io/js/wt.obf.js',
];

async function fetchScript() {
  for (const url of WT_URLS) {
    try {
      const res = await fetch(url, {
        headers: { 'User-Agent': DEFAULT_UA },
        signal: AbortSignal.timeout(10000),
      });
      if (res.ok) {
        const text = await res.text();
        if (text && text.length > 100) return text;
      }
    } catch (err) {
      console.warn(`Failed fetching ${url}: ${err.message}`);
    }
  }
  return null;
}

function extractSaltFromJS(jsCode) {
  try {
    let extractedSalt = null;
    const ctx = {
      navigator: { userAgent: DEFAULT_UA, language: 'en-US' },
      Date: { now: () => 1700000000000 },
      window: {},
      console: console,
    };
    vm.createContext(ctx);
    vm.runInContext(jsCode, ctx, { timeout: 3000 });
    ctx._sha256 = function (str) {
      const parts = String(str).split('::');
      if (parts.length >= 5) {
        extractedSalt = parts[parts.length - 1];
      }
      return 'mock_digest';
    };
    if (typeof ctx.generateWT === 'function') {
      ctx.generateWT('token');
    }
    if (extractedSalt && /^[a-fA-F0-9]{8,64}$/.test(extractedSalt)) {
      return extractedSalt;
    }
  } catch (err) {
    console.warn(`VM extraction error: ${err.message}`);
  }

  // Regex fallback
  const colonMatches = jsCode.match(/::([a-f0-9]{12,32})/gi);
  if (colonMatches) {
    for (const m of colonMatches) {
      const clean = m.replace('::', '').toLowerCase();
      if (clean !== '9844d94d963d30') return clean;
    }
  }

  const quoteMatches = jsCode.match(/["']([a-f0-9]{12,32})["']/gi);
  if (quoteMatches) {
    for (const m of quoteMatches) {
      const clean = m.slice(1, -1).toLowerCase();
      if (clean !== '9844d94d963d30') return clean;
    }
  }

  return null;
}

async function main() {
  console.log('Fetching live GoFile wt.obf.js...');
  const script = await fetchScript();
  if (!script) {
    console.error('Could not retrieve wt.obf.js');
    process.exit(1);
  }

  const salt = extractSaltFromJS(script);
  if (!salt) {
    console.error('Could not extract salt from script');
    process.exit(1);
  }

  console.log(`Discovered active GoFile salt: ${salt}`);

  const confPath = path.resolve(__dirname, '../configs/gallery-dl.conf');
  if (!fs.existsSync(confPath)) {
    console.error(`Config file not found at ${confPath}`);
    process.exit(1);
  }

  const content = fs.readFileSync(confPath, 'utf8');
  const saltRegex = /("salt"\s*:\s*)(?:null|"[^"]*")/;
  if (saltRegex.test(content)) {
    const updated = content.replace(saltRegex, `$1"${salt}"`);
    if (updated !== content) {
      fs.writeFileSync(confPath, updated, 'utf8');
      console.log(`Updated ${confPath} with salt: ${salt}`);
    } else {
      console.log('Salt is already up-to-date in config.');
    }
  } else {
    console.warn('Could not locate "salt" key in gallery-dl.conf');
  }
}

main().catch((err) => {
  console.error('Fatal:', err);
  process.exit(1);
});
