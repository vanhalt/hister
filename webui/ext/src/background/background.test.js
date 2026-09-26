import assert from 'node:assert/strict';
import { test } from 'node:test';
import vm from 'node:vm';
import { fileURLToPath } from 'node:url';
import { build } from 'vite';

const bundle = await build({
  configFile: false,
  logLevel: 'silent',
  build: {
    write: false,
    minify: false,
    rolldownOptions: {
      input: fileURLToPath(new URL('./background.ts', import.meta.url)),
      output: { format: 'iife' },
    },
  },
});
const script = bundle.output.find((entry) => entry.type === 'chunk').code;

function browser({
  rules = {},
  contentScript = true,
  indexingEnabled = true,
  galleryEnabled = true,
  sourceURL = 'https://example.com/visited?tracking=true',
} = {}) {
  const canonicalURL = 'https://example.com/clean';
  const documents = [];
  const lookups = [];
  const uploads = { needed: [], items: [] };
  let onMessage;
  let onActivated;
  const storage = {
    histerURL: 'https://hister.example/',
    showIndexedBadge: true,
    indexingEnabled,
    sendGalleryImages: galleryEnabled,
  };
  const event = { addListener() {} };
  const action = (_, callback) => callback?.();
  vm.runInNewContext(script, {
    URL,
    URLSearchParams,
    console,
    Date,
    setTimeout,
    clearTimeout,
    AbortController,
    btoa,
    atob,
    crypto,
    chrome: {
      runtime: {
        onMessage: { addListener: (fn) => (onMessage = fn) },
        getURL: (path) => `chrome-extension://hister/${path}`,
      },
      tabs: {
        onRemoved: event,
        onUpdated: event,
        onActivated: { addListener: (fn) => (onActivated = fn) },
        get: async () => ({ id: 1, url: sourceURL }),
        sendMessage: async (tabId, request, options) => {
          assert.equal(tabId, 1);
          assert.equal(request.action, 'getPageURL');
          assert.equal(options.frameId, 0);
          if (!contentScript) throw new Error('No content script');
          return { url: canonicalURL };
        },
      },
      storage: {
        onChanged: event,
        local: {
          get: async (_, callback) => {
            const data = structuredClone(storage);
            callback?.(data);
            return data;
          },
        },
      },
      action: {
        setBadgeText: action,
        setBadgeBackgroundColor: action,
        setIcon: action,
      },
    },
    fetch: async (url, options) => {
      const parsed = new URL(url);
      if (parsed.protocol === 'chrome-extension:') throw new Error('No icon in test');
      if (parsed.pathname === '/api/rules') return Response.json(rules);
      if (parsed.pathname === '/api/add') {
        documents.push(JSON.parse(options.body));
        return new Response('', { status: 201 });
      }
      if (parsed.pathname === '/api/image/needed') {
        const { hashes } = JSON.parse(options.body);
        uploads.needed.push(hashes);
        return Response.json({ needed: hashes });
      }
      if (parsed.pathname === '/api/image') {
        uploads.items.push(JSON.parse(options.body));
        return Response.json({ key: 'k', merged: true });
      }
      if (parsed.pathname.startsWith('/gallery/')) {
        if (parsed.pathname.endsWith('/missing.jpg')) {
          return new Response('nope', { status: 404 });
        }
        const match = parsed.pathname.match(/img(\d+)\.jpg$/);
        const variant = match ? Number(match[1]) % 256 : 0;
        return new Response(
          new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, variant, 0x00]),
          { headers: { 'content-type': 'image/png' } },
        );
      }
      if (parsed.pathname === '/api/document') {
        lookups.push(parsed.searchParams.get('url'));
        return new Response('', { status: 200 });
      }
      throw new Error(`Unexpected fetch: ${url}`);
    },
  });
  const message = (request) =>
    new Promise((resolve) =>
      onMessage(request, { url: sourceURL, tab: { id: 1, url: sourceURL } }, resolve),
    );
  return {
    sourceURL,
    canonicalURL,
    documents,
    lookups,
    uploads,
    message,
    activate: () => onActivated({ tabId: 1 }),
    submit: (manual = false) =>
      message({
        pageData: { url: canonicalURL, title: 'Article', text: 'Article text', faviconURL: '' },
        ...(manual ? { action: 'reindex' } : {}),
      }),
  };
}

test('automatic canonical submissions respect rules for both visited and canonical URLs', async () => {
  const cases = [
    { rules: {}, expected: 201 },
    { rules: { skip: ['/visited'] }, expected: 406 },
    { rules: { skip: ['/clean'] }, expected: 406 },
    { rules: { allow: ['/visited'] }, expected: 406 },
    { rules: { allow: ['/clean'] }, expected: 406 },
    { rules: { allow: ['example\\.com'] }, expected: 201 },
  ];
  for (const { rules, expected } of cases) {
    const b = browser({ rules });
    const response = await b.submit();
    assert.equal(response.status_code, expected, JSON.stringify(rules));
    assert.equal(b.documents.length, expected === 201 ? 1 : 0);
    if (b.documents.length) assert.equal(b.documents[0].url, b.canonicalURL);
  }
});

test('manual canonical submissions still bypass rules for the visited URL', async () => {
  const b = browser({ rules: { skip: ['/visited', '/clean'] } });
  const response = await b.submit(true);
  assert.equal(response.status_code, 201);
  assert.equal(b.documents[0].url, b.canonicalURL);
  assert.equal(b.documents[0].metadata.ignore_skip_rules, true);
});

test('visited URL rules continue to ignore fragments', async () => {
  const b = browser({
    sourceURL: 'https://example.com/visited#section',
    rules: { allow: ['/visited$', '/clean$'] },
  });
  assert.equal((await b.submit()).status_code, 201);
});

test('indexed badges look up the canonical URL even with automatic indexing disabled', async () => {
  for (const indexingEnabled of [true, false]) {
    const b = browser({ indexingEnabled });
    await b.activate();
    assert.deepEqual(b.lookups, [b.canonicalURL]);
  }
});

test('indexed badges fall back to the visited URL when no content script is available', async () => {
  const b = browser({ contentScript: false });
  await b.activate();
  assert.deepEqual(b.lookups, [b.sourceURL]);
});

test('popup rule checks include both the visited and canonical URLs', async () => {
  for (const pattern of ['/visited', '/clean']) {
    const b = browser({ rules: { skip: [pattern] } });
    const response = await b.message({
      action: 'checkSkipRule',
      url: b.canonicalURL,
      sourceURL: b.sourceURL,
    });
    assert.equal(response.isSkipped, true);
  }
});

test('gallery candidates submit a manifest and stream uploads in the background', async () => {
  const b = browser();
  const response = await b.message({
    pageData: { url: b.canonicalURL, title: 'Article', text: 'Article text', faviconURL: '' },
    imageCandidates: [
      { url: 'https://example.com/gallery/a.jpg', alt: 'A' },
      { url: 'https://example.com/gallery/missing.jpg', alt: 'Missing' },
    ],
  });
  assert.equal(response.status_code, 201);
  // The page submission carries only the lightweight manifest, never bytes.
  const metadata = b.documents[0].metadata;
  const manifest = JSON.parse(metadata.images);
  assert.equal(manifest.length, 1);
  assert.equal(manifest[0].alt, 'A');
  assert.match(manifest[0].hash, /^[0-9a-f]{64}$/);
  assert.equal(metadata.image_count, 1);
  // Uploads stream one by one after the page submission resolves.
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.deepEqual(b.uploads.needed, [[manifest[0].hash]]);
  assert.equal(b.uploads.items.length, 1);
  assert.equal(b.uploads.items[0].url, b.canonicalURL);
  assert.equal(b.uploads.items[0].alt, 'A');
  assert.equal(b.uploads.items[0].hash, manifest[0].hash);
  assert.ok(b.uploads.items[0].data_uri.startsWith('data:image/png;base64,'));
});

test('gallery downloads are skipped when disabled in settings', async () => {
  const b = browser({ galleryEnabled: false });
  const response = await b.message({
    pageData: { url: b.canonicalURL, title: 'Article', text: 'Article text', faviconURL: '' },
    imageCandidates: [{ url: 'https://example.com/gallery/a.jpg', alt: 'A' }],
  });
  assert.equal(response.status_code, 201);
  assert.equal(b.documents[0].metadata, undefined);
});

test('gallery downloads have no count cap', async () => {
  const b = browser();
  const candidates = Array.from(
    { length: 25 },
    (_, i) => ({ url: `https://example.com/gallery/img${i}.jpg`, alt: `Image ${i}` }),
  );
  const response = await b.message({
    pageData: { url: b.canonicalURL, title: 'Article', text: 'Article text', faviconURL: '' },
    imageCandidates: candidates,
  });
  assert.equal(response.status_code, 201);
  const images = JSON.parse(b.documents[0].metadata.images);
  assert.equal(images.length, 25);
  assert.equal(images[24].alt, 'Image 24');
});
