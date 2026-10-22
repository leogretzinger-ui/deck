// Real HTTP coverage for measured upload bytes and response uncertainty.
// Serves the actual client module; no XHR or network routing mocks.
// The Files panel probe separately checks the built UI.
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { stripTypeScriptTypes } from 'node:module';
import { browser as connect } from './browser.mjs';
const root = resolve(import.meta.dirname, '../src');
const size = 2 * 1024 * 1024, destination = 'docs/a b.bin';
let requests = 0, bytes = 0, held, received;
let bodyRead = new Promise(r => received = r);
const server = createServer(async (req, res) => {
  if (req.method === 'POST') {
    requests++;
    const url = new URL(req.url, 'http://fixture');
    assert.equal(url.pathname, '/api/projects/project%2Fid/files');
    assert.equal(url.searchParams.get('path'), destination);
    assert.equal(req.headers['x-csrf-token'], 'transport-fixture');
    assert.match(req.headers.cookie ?? '', /noisedeck_csrf=transport-fixture/);
    assert.equal(req.headers['content-type'], 'application/octet-stream');
    let current = 0;
    req.on('data', b => { assert(b.every(x => x === 7)); current += b.length; });
    req.on('end', () => { bytes += current; assert.equal(current, size); held = res; received(); });
    return;
  }
  if (req.url.startsWith('/api/projects/project%2Fid/files?')) {
    res.setHeader('Content-Type', 'application/json');
    res.end(JSON.stringify({path:'docs',at_root:false,entries:[]}));
    return;
  }
  if (req.url.startsWith('/src/')) {
    const file = resolve(root, decodeURIComponent(req.url.slice(5)));
    if (!file.startsWith(root + '/')) { res.writeHead(403).end(); return; }
    try {
      res.setHeader('Content-Type', 'application/javascript');
      res.end(stripTypeScriptTypes(await readFile(file, 'utf8')));
    } catch(e) { res.writeHead(500).end(String(e)); }
    return;
  }
  res.setHeader('Content-Type', 'text/html');
  res.setHeader('Set-Cookie', 'noisedeck_csrf=transport-fixture; SameSite=Strict');
  res.end('<!doctype html><title>Measured client upload</title>');
});
await new Promise(r => server.listen(0, '127.0.0.1', r));
let browser, context, page;
try {
  ({ browser } = await connect());
  context = await browser.newContext();
  page = await context.newPage();
  await page.goto(`http://127.0.0.1:${server.address().port}/`);
  const cdp = await context.newCDPSession(page);
  await cdp.send('Network.enable');
  await cdp.send('Network.emulateNetworkConditions', {offline:false,latency:5,downloadThroughput:10485760,uploadThroughput:1048576});
  const start = () => page.evaluate(async ({size,destination}) => {
    const { HttpApiClient, ApiError } = await import('/src/api/client.ts');
    window.observation = { events: [], sent: false, outcome: 'pending' };
    new HttpApiClient().uploadProjectFile('project/id', destination, new Blob([new Uint8Array(size).fill(7)]), undefined, {
      onProgress: (n,total) => window.observation.events.push([n,total]),
      onSent: () => window.observation.sent = true,
    }).then(result => { window.observation.outcome = 'confirmed'; window.observation.result = result; },
      error => { window.observation.outcome = error instanceof ApiError ? 'refused' : 'unconfirmed'; window.observation.error=String(error); });
  }, {size,destination});
  await start();
  await page.waitForFunction(() => window.observation.sent, null, { timeout: 15000 });
  await bodyRead;
  const before = await page.evaluate(() => window.observation);
  assert.equal(before.outcome, 'pending');
  assert(before.events.some(([n,total]) => n > 0 && n < total));
  held.setHeader('Content-Type','application/json');
  held.end(JSON.stringify({path:destination,size}));
  await page.waitForFunction(() => window.observation.outcome !== 'pending');
  const confirmed = await page.evaluate(() => window.observation);
  assert.equal(confirmed.outcome, 'confirmed');
  bodyRead = new Promise(r => received = r);
  await start();
  await bodyRead;
  // Send headers before truncating: a pre-header disconnect can cause
  // Chromium itself to replay a POST on a recovered connection. This
  // checks application retry behavior without asserting browser internals.
  held.writeHead(201, { 'Content-Type': 'application/json', 'Content-Length': '100' });
  held.write('{');
  held.socket.end();
  await page.waitForFunction(() => window.observation.outcome !== 'pending');
  assert.equal(await page.evaluate(() => window.observation.outcome), 'unconfirmed');
  assert.equal(requests, 2);
  // Exercise the real model and DOM with a request whose entire body arrived,
  // while the HTTP server deliberately never confirms it.
  bodyRead = new Promise(r => received = r);
  await page.evaluate(async ({size}) => {
    const { HttpApiClient } = await import('/src/api/client.ts');
    const { FilesPanelModel } = await import('/src/panels/files/files-panel.ts');
    const { FilesPanel } = await import('/src/panels/files/files-panel-dom.ts');
    document.body.innerHTML = '<div id="files"></div>';
    const model = new FilesPanelModel(new HttpApiClient(), 'project/id', '/workspace/project', {
      onChange: () => panel.render(), onProgress: record => panel.progress(record),
    });
    model.dir = 'docs';
    const panel = new FilesPanel(document.getElementById('files'), model);
    window.uploadModel = model;
    window.uploadWork = model.upload([new File([new Uint8Array(size).fill(7)], 'a b.bin')]);
    model.selectUpload(model.uploadRecords[0]);
  }, {size});
  await page.waitForFunction(() => window.uploadModel.uploadRecords[0]?.status === 'confirming', null, {timeout:15000});
  await bodyRead;
  assert.equal(await page.getByRole('button', {name:'Show upload details: 1 active upload', exact:true}).count(), 1);
  await page.getByRole('button', {name:'Stop waiting', exact:true}).click();
  await page.keyboard.press('Enter');
  await page.keyboard.press('Space');
  await page.waitForFunction(() => !window.uploadModel.uploading);
  assert.deepEqual(await page.evaluate(() => ({status:window.uploadModel.uploadRecords[0]?.status,count:window.uploadModel.uploadRecords.length})),
    {status:'unconfirmed',count:1});
  assert.equal(await page.locator('[data-files-upload-state]').innerText(), 'Outcome unknown');
  assert.equal(await page.getByRole('button', {name:'Show upload details: 1 upload to inspect', exact:true}).count(), 1);
  assert.equal(await page.evaluate(() => document.activeElement?.hasAttribute('data-files-upload-state')), true);
  held.end(JSON.stringify({path:destination,size}));
  await page.evaluate(() => window.uploadWork);
  assert.equal(requests, 3);
  console.log(JSON.stringify({actualClient:true,requests,receivedBytes:bytes,partialEvents:before.events.filter(([n,total])=>n>0&&n<total).length,heldResponsePending:true,lostResponse:'unconfirmed',stopWaitingRetainsUnknown:true,applicationRetries:0}));
} catch(e) {
  console.log('observed at failure', {requests,bytes}, await page?.evaluate(() => window.observation).catch(() => null));
  throw e;
} finally {
  await context?.close();
  await browser?.close();
  server.closeAllConnections();
  await new Promise(r => server.close(r));
}
