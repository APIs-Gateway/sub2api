// Remote CI only. Real installed packages and publisher-style bounded workload.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import {createRequire} from 'node:module';
import {fileURLToPath, pathToFileURL} from 'node:url';
import {spawn} from 'node:child_process';
import {performance} from 'node:perf_hooks';

const script = fileURLToPath(import.meta.url);
const frontend = fs.realpathSync(path.resolve(path.dirname(script), '..'));
const repo = path.dirname(frontend);
const names = [
  'decode.valid-ascii-utf8', 'decode.malformed-bom', 'decode.single-pass',
  'query.plus-space-encoded-plus', 'query.duplicates-null-empty-roundtrip',
  'query.decode-false-numbers-booleans', 'query.malformed-tail',
  'decode.publisher-distinct-runs-32000',
];
const hash = bytes => crypto.createHash('sha256').update(bytes).digest('hex');
const save = (directory, name, value) => fs.writeFileSync(path.join(directory, name), JSON.stringify(value, null, 2) + '\n');

function externalDirectory(argument) {
  assert.equal(process.env.GITHUB_ACTIONS, 'true', 'Remote CI is required');
  assert.ok(!process.env.NODE_OPTIONS && !process.env.NODE_PATH, 'External Node preloads/search paths are forbidden');
  assert.ok(argument && path.isAbsolute(argument), 'An absolute external evidence directory is required');
  const requested = path.resolve(argument);
  const parent = fs.realpathSync(path.dirname(requested));
  const destination = path.join(parent, path.basename(requested));
  const relative = path.relative(repo, destination);
  assert.ok(relative === '..' || relative.startsWith('..' + path.sep) || path.isAbsolute(relative), 'Evidence must be outside the checkout');
  fs.mkdirSync(destination, {recursive: false});
  const directory = fs.realpathSync(destination);
  const actualRelative = path.relative(repo, directory);
  assert.ok(actualRelative === '..' || actualRelative.startsWith('..' + path.sep) || path.isAbsolute(actualRelative), 'Canonical evidence must remain outside the checkout');
  return directory;
}

function metadata(directory) {
  const root = fs.realpathSync(directory);
  const raw = fs.readFileSync(path.join(root, 'package.json'));
  return {root, value: JSON.parse(raw), manifestSHA256: hash(raw), rawBase64: raw.toString('base64')};
}

function edge(owner, name) {
  const request = createRequire(path.join(owner.root, 'package.json'));
  const declarations = ['dependencies', 'devDependencies', 'optionalDependencies', 'peerDependencies']
    .filter(field => Object.hasOwn(owner.value[field] ?? {}, name))
    .map(field => ({field, range: owner.value[field][name]}));
  assert.ok(declarations.length > 0, 'Missing declared owner edge: ' + name);
  const search = request.resolve.paths(name);
  assert.ok(Array.isArray(search));
  for (const directory of search) {
    const manifest = path.join(directory, name, 'package.json');
    if (!fs.existsSync(manifest)) continue;
    const target = metadata(path.dirname(manifest));
    assert.equal(target.value.name, name);
    const record = {owner: owner.value.name, ownerVersion: owner.value.version ?? null,
      ownerRoot: owner.root, ownerManifestSHA256: owner.manifestSHA256,
      requested: name, declarations, nodeSearchPaths: search, targetRoot: target.root,
      targetManifestSHA256: target.manifestSHA256, targetVersion: target.value.version};
    if (name === 'query-string' || name === 'decode-uri-component') {
      // These exact publisher versions expose the same unconditional default
      // entry to require.resolve and ESM import; do not guess a manifest main.
      assert.equal(target.value.exports.default, './index.js');
      record.actualEntry = fs.realpathSync(request.resolve(name));
      assert.equal(record.actualEntry, path.join(target.root, 'index.js'));
      record.actualEntrySHA256 = hash(fs.readFileSync(record.actualEntry));
      record.importURL = pathToFileURL(record.actualEntry).href;
    }
    return {target, record};
  }
  throw new Error('Actual installed owner dependency missing: ' + name);
}

async function installed() {
  const root = metadata(frontend);
  const icons = edge(root, '@lobehub/icons');
  const ui = edge(icons.target, '@lobehub/ui');
  const query = edge(ui.target, 'query-string');
  const decoder = edge(query.target, 'decode-uri-component');
  assert.equal(icons.target.value.version, '4.0.2');
  assert.equal(ui.target.value.version, '4.9.2');
  assert.equal(query.target.value.version, '9.3.1');
  assert.equal(decoder.target.value.version, '0.5.1');
  const installedRoot = fs.realpathSync(path.join(frontend, 'node_modules'));
  for (const item of [icons.target, ui.target, query.target, decoder.target]) {
    assert.ok(item.root.startsWith(installedRoot + path.sep), 'Dependency escaped the installed frontend');
  }
  const physical = new Map();
  const seen = new Set();
  let manifestsVisited = 0;
  function candidate(directory) {
    if (!fs.existsSync(path.join(directory, 'package.json'))) return;
    const item = metadata(directory);
    manifestsVisited++;
    if (item.value.name === 'decode-uri-component') physical.set(item.root, item);
  }
  function walk(directory) {
    const real = fs.realpathSync(directory);
    if (seen.has(real)) return;
    seen.add(real);
    const entries = fs.readdirSync(directory, {withFileTypes: true});
    if (path.basename(directory) === 'node_modules') {
      for (const item of entries) {
        if (item.name.startsWith('.')) continue;
        const itemPath = path.join(directory, item.name);
        if (item.name.startsWith('@')) {
          for (const scoped of fs.readdirSync(itemPath)) candidate(path.join(itemPath, scoped));
        } else if (item.isDirectory() || item.isSymbolicLink()) candidate(itemPath);
      }
    }
    for (const item of entries) {
      if (item.isDirectory() && !item.isSymbolicLink()) walk(path.join(directory, item.name));
    }
  }
  walk(installedRoot);
  assert.ok(physical.has(decoder.target.root));
  for (const item of physical.values()) assert.equal(item.value.version, '0.5.1');
  const decodeModule = await import(decoder.record.importURL);
  const queryModule = await import(query.record.importURL);
  assert.equal(typeof decodeModule.default, 'function');
  assert.equal(typeof queryModule.default.parse, 'function');
  assert.equal(typeof queryModule.default.stringify, 'function');
  return {decode: decodeModule.default, query: queryModule.default,
    discovery: {edges: [icons.record, ui.record, query.record, decoder.record],
      manifests: [root, icons.target, ui.target, query.target, decoder.target],
      physicalInstances: [...physical.values()], scan: {installedRoot, directoriesVisited: seen.size, manifestsVisited},
      interpretation: 'Actual owner-context installed dependency resolution and ESM import. Icons/UI manifests are read; their runtime entries are not evaluated. Application exploit reachability is not asserted.'}};
}

function queryEqual(query, input, expected, options) {
  const actual = query.parse(input, options);
  assert.equal(Object.getPrototypeOf(actual), null);
  assert.deepEqual({...actual}, expected);
  return {input, options: options ?? {}, actual: {...actual}};
}

async function longChild(directory) {
  const {decode, query, discovery} = await installed();
  let input = '';
  let expected = '';
  // This is the publisher's exact 32,000-distinct-run construction, not a
  // production request. The final malformed tail forces the fallback path.
  for (let index = 0; index < 32_000; index++) {
    const high = Math.floor(index / 256).toString(16).padStart(2, '0');
    const low = (index % 256).toString(16).padStart(2, '0');
    input += '%' + high + '%' + low + 'x';
    // In this fixed input domain high is always ASCII (0..124). The publisher
    // preserves these lowercase orphan non-ASCII bytes (its special uppercase
    // %C2 replacement does not apply). This is not a general fallback decoder.
    const byte = index % 256;
    expected += String.fromCharCode(Math.floor(index / 256))
      + (byte < 128 ? String.fromCharCode(byte) : '%' + low) + 'x';
  }
  input += '%zz';
  expected += '%zz';
  fs.writeFileSync(path.join(directory, 'publisher-input.txt'), input);
  const start = performance.now();
  const actual = decode(input);
  const decodeMilliseconds = performance.now() - start;
  assert.equal(typeof actual, 'string');
  assert.equal(actual, expected);
  assert.ok(actual.startsWith('\u0000\u0000x\u0000\u0001x'));
  assert.ok(actual.endsWith('|%ffx%zz'));
  const queryStart = performance.now();
  const parsed = query.parse('payload=' + input);
  const queryMilliseconds = performance.now() - queryStart;
  assert.equal(Object.getPrototypeOf(parsed), null);
  assert.deepEqual(Object.keys(parsed), ['payload']);
  assert.equal(parsed.payload, actual);
  fs.writeFileSync(path.join(directory, 'actual-decoded.txt'), actual);
  save(directory, 'discovery.json', discovery);
  const result = {naturalCompletion: true, distinctRuns: 32000, inputBytes: Buffer.byteLength(input),
    inputSHA256: hash(input), outputBytes: Buffer.byteLength(actual), outputSHA256: hash(actual),
    decodeMilliseconds, queryMilliseconds, directAndRealQueryEqual: true};
  save(directory, 'result.json', result);
  process.stdout.write(JSON.stringify(result) + '\n');
}

async function boundedChild(directory, discovery) {
  const childDirectory = path.join(directory, 'publisher-child');
  const argv = [script, '--long-child', childDirectory];
  const record = {argv: [process.execPath, ...argv], started: false, actualExit: null,
    signal: null, watchdog: false, streamsComplete: false, timeoutMilliseconds: 10000,
    outputLimitBytes: 65536, outputLimitExceeded: false, stdoutTotalBytes: 0, stderrTotalBytes: 0,
    stdoutTruncated: false, stderrTruncated: false};
  const stdout = [];
  const stderr = [];
  let remaining = record.outputLimitBytes;
  const start = performance.now();
  await new Promise(resolve => {
    const child = spawn(process.execPath, argv, {stdio: ['ignore', 'pipe', 'pipe'], detached: process.platform !== 'win32'});
    child.once('spawn', () => { record.started = true; record.pid = child.pid; });
    function killOwnedChild() {
      try {
        if (process.platform === 'win32') child.kill('SIGKILL');
        else process.kill(-child.pid, 'SIGKILL');
      } catch (error) { record.terminationError = String(error); }
    }
    function retain(stream, chunks, value) {
      record[stream + 'TotalBytes'] += value.length;
      const retained = Math.min(value.length, remaining);
      if (retained > 0) chunks.push(Buffer.from(value.subarray(0, retained)));
      remaining -= retained;
      if (retained < value.length) record[stream + 'Truncated'] = true;
      if (record.stdoutTotalBytes + record.stderrTotalBytes > record.outputLimitBytes && !record.outputLimitExceeded) {
        record.outputLimitExceeded = true;
        killOwnedChild();
      }
    }
    child.stdout.on('data', value => retain('stdout', stdout, value));
    child.stderr.on('data', value => retain('stderr', stderr, value));
    child.once('error', error => { record.launchError = String(error); });
    const timer = setTimeout(() => {
      record.watchdog = true;
      killOwnedChild();
    }, 10000);
    child.once('close', (code, signal) => {
      clearTimeout(timer);
      record.actualExit = code;
      record.signal = signal;
      record.transportClosed = true;
      record.streamsComplete = !record.outputLimitExceeded && !record.stdoutTruncated && !record.stderrTruncated;
      resolve();
    });
  });
  const out = Buffer.concat(stdout);
  const err = Buffer.concat(stderr);
  fs.writeFileSync(path.join(directory, 'publisher-child.stdout'), out);
  fs.writeFileSync(path.join(directory, 'publisher-child.stderr'), err);
  Object.assign(record, {elapsedMilliseconds: performance.now() - start,
    stdoutBytes: out.length, stdoutSHA256: hash(out), stderrBytes: err.length, stderrSHA256: hash(err)});
  save(directory, 'publisher-child.process.json', record);
  assert.ok(record.started && record.streamsComplete && !record.watchdog && !record.outputLimitExceeded
    && !record.launchError && !record.terminationError);
  assert.equal(record.stdoutTotalBytes, out.length);
  assert.equal(record.stderrTotalBytes, err.length);
  assert.equal(record.actualExit, 0);
  assert.equal(record.signal, null);
  assert.ok(record.elapsedMilliseconds < 10000, 'Natural completion exceeded the owned-child bound');
  assert.equal(err.length, 0);
  const result = JSON.parse(out.toString('utf8'));
  assert.deepEqual(result, JSON.parse(fs.readFileSync(path.join(childDirectory, 'result.json'), 'utf8')));
  assert.deepEqual(JSON.parse(fs.readFileSync(path.join(childDirectory, 'discovery.json'), 'utf8')), discovery);
  assert.equal(result.naturalCompletion, true);
  assert.equal(result.distinctRuns, 32000);
  assert.equal(result.inputBytes, 224003);
  assert.equal(hash(fs.readFileSync(path.join(childDirectory, 'publisher-input.txt'))), result.inputSHA256);
  assert.equal(hash(fs.readFileSync(path.join(childDirectory, 'actual-decoded.txt'))), result.outputSHA256);
  return {process: record, result};
}

async function main(directory) {
  const report = {groups: [], errors: [], names, scope: 'Installed package contracts only; no application exploit/production HTTP reachability or complexity-proof claim.'};
  try {
    const {decode, query, discovery} = await installed();
    save(directory, 'discovery.json', discovery);
    const groups = [
      () => {
        const cases = [['', ''], ['test', 'test'], ['a+b', 'a+b'], ['%20', ' '], ['%2B', '+'], ['%00', '\0'], ['%7F', '\u007F'], ['%61%62%63', 'abc'], ['%E4%BD%A0%E5%A5%BD', '你好'], ['%F0%9F%98%80', '😀'], ['%F4%8F%BF%BF', '\u{10FFFF}']];
        return cases.map(([input, expected]) => { const actual = decode(input); assert.equal(actual, expected); assert.equal(actual, decodeURIComponent(input)); return {input, expected, actual}; });
      },
      () => {
        const cases = [['%', '%'], ['%G0', '%G0'], ['%C3', '%C3'], ['%C3%41', '%C3A'], ['%C2x', '\uFFFDx'], ['%FE%FF', '\uFFFD\uFFFD'], ['%FF%FE', '\uFFFD\uFFFD'], ['%EF%BB%BFtest', '\uFEFFtest'], ['%C3%A5%ab', 'å%ab']];
        return cases.map(([input, expected]) => { const actual = decode(input); assert.equal(actual, expected); return {input, expected, actual}; });
      },
      () => {
        const cases = [['%252525', '%2525'], ['%25C2%zz', '%C2%zz'], ['%25%41%zz', '%A%zz'], ['%84%D7%25%88%90', '%84%D7%%88%90']];
        return cases.map(([input, expected]) => { const actual = decode(input); assert.equal(actual, expected); return {input, expected, actual}; });
      },
      () => queryEqual(query, 'space=a+b&plus=%2B&literal=%252B', {space: 'a b', plus: '+', literal: '%2B'}),
      () => {
        const parsed = queryEqual(query, 'tag=a&tag=b&flag&empty=&%E4%BD%A0=ok', {tag: ['a', 'b'], flag: null, empty: '', '你': 'ok'});
        const serialized = query.stringify(parsed.actual);
        assert.equal(serialized, 'empty=&flag&tag=a&tag=b&%E4%BD%A0=ok');
        queryEqual(query, serialized, parsed.actual);
        return {...parsed, serialized};
      },
      () => [queryEqual(query, 'x=a+b&n=%32&flag', {x: 'a+b', n: '%32', flag: null}, {decode: false}),
        queryEqual(query, 'n=42&f=false&t=TRUE&empty=&flag&word=text', {n: 42, f: false, t: true, empty: '', flag: null, word: 'text'}, {parseNumbers: true, parseBooleans: true})],
      () => queryEqual(query, 'good=%E4%BD%A0&bad=%C3%41&once=%25C2%zz&bom=%FE%FF&tail=%zz', {good: '你', bad: '%C3A', once: '%C2%zz', bom: '\uFFFD\uFFFD', tail: '%zz'}),
      () => boundedChild(directory, discovery),
    ];
    assert.equal(groups.length, names.length);
    for (let index = 0; index < groups.length; index++) {
      const record = {name: names[index], status: 'RUN'};
      process.stdout.write(JSON.stringify({name: record.name, action: 'RUN'}) + '\n');
      try {
        record.observations = await groups[index]();
        record.status = 'PASS';
      } catch (error) {
        record.status = 'FAIL';
        record.error = error.stack ?? String(error);
        report.errors.push({name: record.name, error: record.error});
      }
      report.groups.push(record);
      process.stdout.write(JSON.stringify({name: record.name, action: record.status}) + '\n');
    }
  } catch (error) { report.errors.push({prerequisite: true, error: error.stack ?? String(error)}); }
  save(directory, 'contracts.json', report);
  assert.deepEqual(report.groups.map(group => group.name), names);
  assert.equal(report.errors.length, 0, JSON.stringify(report.errors));
  assert.ok(report.groups.every(group => group.status === 'PASS'));
}

try {
  const child = process.argv[2] === '--long-child';
  const directory = externalDirectory(process.argv[child ? 3 : 2]);
  if (child) await longChild(directory);
  else await main(directory);
} catch (error) {
  process.stderr.write((error.stack ?? String(error)) + '\n');
  process.exitCode = 1;
}
