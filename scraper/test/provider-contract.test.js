import test from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

import { listProviders } from '../providers/index.js';

const PROVIDERS_DIR = join(dirname(fileURLToPath(import.meta.url)), '..', 'providers');

const NOT_PROVIDERS = new Set(['index.js', '_template.js']);

const providerFiles = readdirSync(PROVIDERS_DIR)
  .filter(f => f.endsWith('.js') && !NOT_PROVIDERS.has(f))
  .sort();

const modules = await Promise.all(
  providerFiles.map(async file => ({ file, mod: await import(join(PROVIDERS_DIR, file)) }))
);

test('there is at least one provider', () => {
  assert.ok(providerFiles.length > 0);
});

for (const { file, mod } of modules) {
  test(`${file} exports a string id`, () => {
    assert.equal(typeof mod.id, 'string');
    assert.ok(mod.id.length > 0);
  });

  test(`${file} id is lowercase with no whitespace`, () => {
    assert.equal(mod.id, mod.id.toLowerCase());
    assert.doesNotMatch(mod.id, /\s/);
  });

  test(`${file} exports a non-empty name`, () => {
    assert.equal(typeof mod.name, 'string');
    assert.ok(mod.name.trim().length > 0);
  });

  test(`${file} exports an async scrape function`, () => {
    assert.equal(typeof mod.scrape, 'function');
    assert.equal(mod.scrape.constructor.name, 'AsyncFunction');
  });

  test(`${file} scrape takes the meta object`, () => {
    assert.ok(mod.scrape.length >= 1);
  });
}

test('provider ids are unique', () => {
  const byId = new Map();
  for (const { file, mod } of modules) {
    if (byId.has(mod.id)) {
      assert.fail(`id "${mod.id}" is used by both ${byId.get(mod.id)} and ${file}`);
    }
    byId.set(mod.id, file);
  }
});

test('every provider file is registered in the aggregator', () => {
  if (process.env.SCRAPER_DISABLED_PROVIDERS) {
    return;
  }

  const registered = new Set(listProviders().map(p => p.id));
  const missing = modules
    .filter(({ mod }) => !registered.has(mod.id))
    .map(({ file, mod }) => `${file} (id: ${mod.id})`);

  assert.deepEqual(missing, []);
});

test('the aggregator reports the same id and name as each module', () => {
  const registered = new Map(listProviders().map(p => [p.id, p.name]));
  for (const { file, mod } of modules) {
    if (!registered.has(mod.id)) continue;
    assert.equal(registered.get(mod.id), mod.name);
  }
});

test('the _template provider still satisfies the contract it documents', async () => {
  const tpl = await import(join(PROVIDERS_DIR, '_template.js'));
  assert.equal(typeof tpl.id, 'string');
  assert.equal(typeof tpl.name, 'string');
  assert.equal(tpl.scrape.constructor.name, 'AsyncFunction');
  assert.deepEqual(await tpl.scrape({}), []);
});

test('the shared extractInfoHash normalises base32 to hex', async () => {
  const { extractInfoHash } = await import(join(PROVIDERS_DIR, '..', 'lib', 'magnetHelper.js'));

  const hex = '6383139c533b515314c7bb5a6933ccc4899f0825';
  assert.equal(
    extractInfoHash('magnet:?xt=urn:btih:MOBRHHCTHNIVGFGHXNNGSM6MYSEZ6CBF&tr=http://x/announce'),
    hex
  );
  assert.equal(
    extractInfoHash(`magnet:?xt=urn:btih:${hex.toUpperCase()}`),
    hex
  );
  assert.equal(extractInfoHash('magnet:?dn=no-hash-here'), null);
  assert.equal(extractInfoHash(''), null);
});

test('every provider declares the content types it serves', () => {
  const VALID = ['movie', 'series', 'anime'];

  for (const { file, mod } of modules) {
    assert.ok(
      Array.isArray(mod.types) && mod.types.length,
      `${file}: missing the \`types\` export.`
    );
    for (const t of mod.types) {
      assert.ok(VALID.includes(t));
    }
    assert.equal(
      new Set(mod.types).size,
      mod.types.length
    );
  }
});

test('the aggregator reports each provider the types its module declares', () => {
  const registered = new Map(listProviders().map(p => [p.id, p.types]));

  for (const { file, mod } of modules) {
    if (!registered.has(mod.id)) continue;
    assert.deepEqual(registered.get(mod.id), mod.types);
  }
});

test('a provider returns nothing for a type it does not declare', async () => {
  const ALL = ['movie', 'series', 'anime'];
  const skipped = [];

  for (const { file, mod } of modules) {
    for (const type of ALL) {
      if (mod.types.includes(type)) continue;
      const out = await mod.scrape({
        type, name: 'The Shawshank Redemption', year: 1994, imdbId: 'tt0111161',
      });
      assert.deepEqual(out, []);
      skipped.push(`${mod.id}/${type}`);
    }
  }

  assert.ok(skipped.length);
});

