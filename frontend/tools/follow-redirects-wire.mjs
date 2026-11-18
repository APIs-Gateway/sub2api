// Run only in remote CI against the actual frozen axios transitive dependency.
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { createRequire } from 'node:module';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

const cases = [
  { name: 'object-builtin-same', array: false, custom: false, cross: false },
  { name: 'array-builtin-same', array: true, custom: false, cross: false },
  { name: 'object-builtin-cross', array: false, custom: false, cross: true },
  { name: 'array-builtin-cross', array: true, custom: false, cross: true },
  { name: 'object-custom-same', array: false, custom: true, cross: false },
  { name: 'array-custom-same', array: true, custom: true, cross: false },
  { name: 'object-custom-cross', array: false, custom: true, cross: true },
  { name: 'array-custom-cross', array: true, custom: true, cross: true },
  { name: 'relative-normal-redirect', array: false, normal: true, cross: false },
];
const expectedIndexSHA256 = '656e169cc2e8043ec00b7d912f3ff523a01026a3fd38e04516b37fb39be94f14';
const frontend = path.resolve(__dirname, '..');
const evidence = process.env.FOLLOW_REDIRECTS_EVIDENCE;
const sha256 = data => crypto.createHash('sha256').update(data).digest('hex');
const report = { expectedCases: cases.map(test => test.name), results: [], observations: [], passed: false };
const servers = [];
const sockets = new Set();

function listen(server) {
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => {
      server.removeListener('error', reject);
      resolve(`http://127.0.0.1:${server.address().port}`);
    });
  });
}

function request(library, url, options) {
  return new Promise((resolve, reject) => {
    let request;
    const watchdog = setTimeout(() => {
      request?.destroy(new Error('wire contract exceeded 5 seconds'));
      reject(new Error('wire contract exceeded 5 seconds'));
    }, 5000);
    const finish = (error, response) => {
      clearTimeout(watchdog);
      if (error) reject(error); else resolve(response);
    };
    try {
      request = library.http.get(url, { ...options, maxRedirects: 2 }, response => {
        let body = '';
        response.setEncoding('utf8');
        response.on('data', chunk => { body += chunk; });
        response.once('error', error => finish(error));
        response.once('end', () => finish(null, { status: response.statusCode, body }));
        response.once('aborted', () => finish(new Error('final response aborted')));
      });
      request.once('error', error => finish(error));
    } catch (error) {
      finish(error);
    }
  });
}

async function main() {
  assert.equal(process.env.GITHUB_ACTIONS, 'true', 'remote GitHub CI only; never local or server87');
  assert.ok(evidence, 'fresh owned evidence directory required');
  assert.equal(fs.existsSync(evidence), false, 'stale evidence directory');
  fs.mkdirSync(evidence, { recursive: true });
  try {
    const frontendRequire = createRequire(path.join(frontend, 'package.json'));
    const axiosManifest = frontendRequire.resolve('axios/package.json');
    const axiosRequire = createRequire(axiosManifest);
    const indexPath = axiosRequire.resolve('follow-redirects');
    const packagePath = axiosRequire.resolve('follow-redirects/package.json');
    const index = fs.readFileSync(indexPath);
    const packageBytes = fs.readFileSync(packagePath);
    const manifest = JSON.parse(packageBytes);
    const axios = JSON.parse(fs.readFileSync(axiosManifest));
    const git = (...args) => execFileSync('git', args, { cwd: frontend, encoding: 'utf8' }).trim();
    report.source = {
      head: git('rev-parse', 'HEAD'), expectedHead: process.env.EXPECTED_SOURCE_HEAD,
      tree: git('rev-parse', 'HEAD^{tree}'),
      nodeVersion: process.version, axiosVersion: axios.version,
      axiosManifest, indexPath, resolvedIndexPath: fs.realpathSync(indexPath), packagePath,
      followRedirectsVersion: manifest.version, indexSHA256: sha256(index),
      expectedOfficialIndexSHA256: expectedIndexSHA256, packageSHA256: sha256(packageBytes),
      lockSHA256: sha256(fs.readFileSync(path.join(frontend, 'pnpm-lock.yaml'))),
      testSHA256: sha256(fs.readFileSync(__filename)),
      officialFixCommits: ['9865ca45490e4d0f7b6944870318408344570aff', '080a76d0e125b37bf81717368a61d5dc37aa56c8'],
      verifiedOfficialTarballSHA256: '6e075d999dc5ff568916954508397b11a537b18bde615986d7bd8b3d3fa1799f',
      note: 'Tarball digest is the independently verified official distribution reference; this run verifies installed index bytes, not a second downloaded tarball.',
    };
    assert.equal(report.source.head, process.env.EXPECTED_SOURCE_HEAD, 'actual checkout differs from exact event head');
    fs.writeFileSync(path.join(evidence, 'installed-index.js'), index);
    fs.writeFileSync(path.join(evidence, 'installed-package.json'), packageBytes);
    assert.equal(axios.version, '1.20.0');
    assert.equal(manifest.version, '1.16.1');
    assert.equal(sha256(index), expectedIndexSHA256, 'installed implementation differs from verified official patch bytes');
    const library = axiosRequire('follow-redirects');
    let originA;
    let originB;
    for (const label of ['A', 'B']) {
      const server = http.createServer((incoming, response) => {
        const url = new URL(incoming.url, 'http://127.0.0.1');
        const name = url.pathname.split('/')[1];
        const test = cases.find(candidate => candidate.name === name);
        report.observations.push({ server: label, case: name, path: url.pathname,
          method: incoming.method, rawHeaders: [...incoming.rawHeaders], headers: { ...incoming.headers } });
        incoming.resume();
        if (!test) { response.writeHead(404).end('unknown'); return; }
        if (url.pathname === `/${name}/start` && label === 'A') {
          const location = test.normal ? `/${name}/final` : `${test.cross ? originB : originA}/${name}/final`;
          response.writeHead(302, { Location: location }).end('redirect');
        } else if (url.pathname === `/${name}/final`) {
          response.writeHead(200, { 'Content-Type': 'text/plain' }).end('wire-complete');
        } else {
          response.writeHead(404).end('unexpected hop');
        }
      });
      server.on('connection', socket => { sockets.add(socket); socket.once('close', () => sockets.delete(socket)); });
      servers.push(server);
      const origin = await listen(server);
      if (label === 'A') originA = origin; else originB = origin;
    }
    assert.notEqual(originA, originB, 'cross-origin must use a distinct actual listener');
    report.origins = { A: originA, B: originB };
    for (const test of cases) {
      const result = { name: test.name, passed: false };
      report.results.push(result);
      try {
        const secretNames = test.normal ? [] : test.custom
          ? ['X-Private^Key', 'X-Private|Key'] : ['Authorization', 'Proxy-Authorization', 'Cookie'];
        const pairs = [['X-Control', `ordinary-${test.name}`], ...secretNames.map(name => [name, `secret-${test.name}`])];
        const headers = test.array ? pairs.flat() : Object.fromEntries(pairs);
        const response = await request(library, `${originA}/${test.name}/start`, {
          headers, sensitiveHeaders: test.custom ? secretNames : [],
        });
        const observed = report.observations.filter(row => row.case === test.name);
        assert.equal(response.status, 200);
        assert.equal(response.body, 'wire-complete');
        assert.equal(observed.length, 2, 'exactly two real HTTP requests required');
        assert.deepEqual(observed.map(row => [row.server, row.path, row.method]), [
          ['A', `/${test.name}/start`, 'GET'],
          [test.cross ? 'B' : 'A', `/${test.name}/final`, 'GET'],
        ]);
        for (const [name, value] of pairs) {
          assert.equal(observed[0].headers[name.toLowerCase()], value, `initial server did not receive ${name}`);
          const expected = name === 'X-Control' || !test.cross ? value : undefined;
          assert.equal(observed[1].headers[name.toLowerCase()], expected, `final server header contract: ${name}`);
        }
        result.passed = true;
      } catch (error) {
        result.error = error.stack || String(error);
      }
    }
    assert.equal(report.results.length, 9);
    assert.equal(report.observations.length, 18, 'complete actual two-hop inventory');
    assert.equal(report.results.filter(result => result.passed).length, 9, 'one or more real header contracts failed');
    report.passed = true;
  } catch (error) {
    report.error = error.stack || String(error);
    process.exitCode = 1;
  } finally {
    for (const socket of sockets) socket.destroy();
    await Promise.all(servers.map(server => new Promise(resolve => server.close(resolve))));
    fs.writeFileSync(path.join(evidence, 'actual-wire-results.json'), JSON.stringify(report, null, 2) + '\n');
    console.log(JSON.stringify({ passed: report.passed, cases: report.results.length,
      passedCases: report.results.filter(result => result.passed).length, actualRequests: report.observations.length }));
  }
}

main().catch(error => { console.error(error.stack || error); process.exitCode = 1; });
