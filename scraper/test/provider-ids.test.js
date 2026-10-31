process.env.SCRAPER_DISABLED_PROVIDERS = 'yts,glotorrents';

import test from 'node:test';
import assert from 'node:assert/strict';

const { classifyProviderIds, listProviders } = await import('../providers/index.js');

test('an id this host disabled is reported as disabled, never as unknown', () => {
  const { active, disabled, unknown } = classifyProviderIds(['yts']);

  assert.deepEqual(disabled, ['yts']);
  assert.deepEqual(unknown, []);
  assert.deepEqual(active, []);
});

test('an id that was never registered is unknown', () => {
  const { active, disabled, unknown } = classifyProviderIds(['nope']);

  assert.deepEqual(unknown, ['nope']);
  assert.deepEqual(disabled, []);
  assert.deepEqual(active, []);
});

test('a request mixing all three cases keeps them apart', () => {
  const { active, disabled, unknown } = classifyProviderIds(['eztv', 'yts', 'nope']);

  assert.deepEqual(active, ['eztv']);
  assert.deepEqual(disabled, ['yts']);
  assert.deepEqual(unknown, ['nope']);
});

test('one bad id does not discard the valid ones', () => {
  const { active } = classifyProviderIds(['eztv', 'nyaa', 'nope']);

  assert.deepEqual(active, ['eztv', 'nyaa']);
});

test('1337x registers under its site name, not its module name', () => {
  const { active, unknown } = classifyProviderIds(['1337x', 'leetx']);

  assert.deepEqual(active, ['1337x']);
  assert.deepEqual(unknown, ['leetx']);
});

test('an empty request classifies as nothing at all', () => {
  assert.deepEqual(classifyProviderIds([]), { active: [], disabled: [], unknown: [] });
});

test('listProviders hides what this host disabled', () => {
  const ids = listProviders().map(p => p.id);

  assert.ok(!ids.includes('yts'));
  assert.ok(!ids.includes('glotorrents'));
  assert.ok(ids.includes('eztv'));
});

