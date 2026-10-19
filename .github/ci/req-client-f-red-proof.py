#!/usr/bin/env python3
"""Remote CI only: genuine, sealed F44 observations; never supplies test events."""
import collections
import copy
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys

BASE = 'c541f5a171fe85a053a6199e10304a386e852d22'
CANDIDATE = '0eaabfbe18250716fb6017ec0b068d47bf7484a2'
BASE_TREE = '09e841970f893e41c4a8e4a02c8059545a03f238'
CANDIDATE_TREE = '7370b573c6f2cca0de88aa85eb5c358246114b9b'
MODULE = 'github.com/imroc/req/v3'
PREFIX = 'backend/third_party/req/'
ADDED = {
    PREFIX + name for name in (
        'client_behavior_peer_test.go', 'client_behavior_observer_test.go',
        'client_behavior_contract_test.go', 'client_redirect_cookie_contract_test.go',
        'client_response_contract_test.go', 'CLIENT-BEHAVIOR-PROVENANCE.json',
        'CLIENT-BEHAVIOR-REVIEW.md', 'CLIENT-BEHAVIOR-LICENSE')
}
# These are unproven hypotheses until actual Go FAIL events have the specified
# marker. Any fixture/compile/race/unexpected error fails this controller.
RED = {
    'TestReqClientDigest_NoChallengeEvictsCache': 'REQ_DIGEST_401_BODY_ASSERT:',
    'TestReqClientDigest_GetBodyFailure/clone_1': 'REQ_DIGEST_BODY_CLOSE_ASSERT:',
    'TestReqClientDigest_GetBodyFailure/clone_2': 'REQ_DIGEST_BODY_CLOSE_ASSERT:',
    'TestReqClientDigest_ChallengeBoundary/basic_only': 'REQ_DIGEST_401_BODY_ASSERT:',
    'TestReqClientDigest_ChallengeBoundary/unsupported_digest': 'REQ_DIGEST_401_BODY_ASSERT:',
}
ROOTS = ('TestReqClientDigest_', 'TestReqClientRedirect_', 'TestReqClientCookie_', 'TestReqClientResponse_')


def require(value, message):
    if not value:
        raise AssertionError(message)


def git(repo, *args):
    return subprocess.check_output(['git', '-C', str(repo), *args])


def sha(data):
    return hashlib.sha256(data).hexdigest()


def save(evidence, name, value):
    (evidence / name).write_text(json.dumps(value, indent=2, sort_keys=True) + '\n')


def freeze(repo, head, tree):
    require(git(repo, 'rev-parse', 'HEAD').decode().strip() == head, 'checkout HEAD differs')
    require(git(repo, 'rev-parse', 'HEAD^{tree}').decode().strip() == tree, 'checkout tree differs')
    require(not git(repo, 'status', '--porcelain', '--untracked-files=all'), 'dirty tracked/untracked inputs')
    result = []
    for record in git(repo, 'ls-files', '--stage', '-z').split(b'\0'):
        if not record:
            continue
        meta, rawname = record.split(b'\t', 1)
        mode, blob, stage = meta.decode().split()
        require(stage == '0' and mode in ('100644', '100755', '120000'), 'nonregular Git input')
        name = rawname.decode()
        path = repo / name
        data = os.fsencode(os.readlink(path)) if mode == '120000' else path.read_bytes()
        require(hashlib.sha1(b'blob ' + str(len(data)).encode() + b'\0' + data).hexdigest() == blob,
                'input Git blob differs: ' + name)
        require(mode == '120000' or bool(path.stat().st_mode & 0o111) == (mode == '100755'),
                'input execute mode differs: ' + name)
        result.append({'path': name, 'mode': mode, 'gitBlob': blob, 'bytes': len(data), 'sha256': sha(data)})
    require(any(x['path'].startswith('frontend/') for x in result), 'full repository fixtures missing')
    return {'head': head, 'tree': tree, 'inputs': result}


def setup(baseline, candidate, evidence):
    require(not evidence.exists(), 'owned evidence directory already exists')
    evidence.mkdir(parents=True)
    frozen = {}
    for phase, repo, head, tree in (
            ('baseline', baseline, BASE, BASE_TREE), ('candidate', candidate, CANDIDATE, CANDIDATE_TREE)):
        frozen[phase] = freeze(repo, head, tree)
        ignored = git(repo, 'ls-files', '--others', '--ignored', '--exclude-standard', '-z')
        require(not ignored, phase + ' ignored inputs before execution')
        save(evidence, phase + '-frozen.json', frozen[phase])
        (evidence / (phase + '-index.txt')).write_bytes(git(repo, 'ls-files', '--stage'))
    old = {x['path']: x for x in frozen['baseline']['inputs']}
    new = {x['path']: x for x in frozen['candidate']['inputs']}
    require(new.keys() - old.keys() == ADDED and not old.keys() - new.keys(), 'candidate path scope differs')
    require(all(old[p] == new[p] for p in old), 'candidate modified an existing baseline input')
    manifest = json.loads((candidate / PREFIX / 'UPSTREAM-MANIFEST.json').read_text())
    require(len(manifest['originalFiles']) == 132, 'original132 missing')
    originals = [PREFIX + x['path'] for x in manifest['originalFiles']]
    require(sum(p.endswith('_test.go') for p in originals) == 14, 'original14 tests missing')
    require(all(old[p] == new[p] for p in originals), 'original baseline source/test changed')
    provenance = json.loads((candidate / PREFIX / 'CLIENT-BEHAVIOR-PROVENANCE.json').read_text())
    require(provenance['newNamedLeafCount'] == 44 and len(set(provenance['newNamedLeaves'])) == 44,
            'F44 frozen inventory differs')
    require(set(RED) <= set(provenance['newNamedLeaves']), 'RED witness missing from F44')
    for item in provenance['frozenRawFiles']:
        data = (candidate / PREFIX / item['path']).read_bytes()
        require(len(data) == item['bytes'] and sha(data) == item['sha256'], 'formatted source seal differs')
    require(sha((candidate / PREFIX / 'client_behavior_peer_test.go').read_bytes()) ==
            '0e6458474cec81c56a1fc29141811a02c7b256952d45c1118dd3c1934da9870b', 'original peer differs')
    for path in ADDED:
        destination = evidence / 'candidate-sources' / path
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(candidate / path, destination)
    save(evidence, 'setup-proof.json', {'baseline': BASE, 'candidate': CANDIDATE,
         'addedPaths': sorted(ADDED), 'unchangedBaselineInputs': len(old), 'originalFiles': 132,
         'originalTestFiles': 14, 'requiredFLeaves': provenance['newNamedLeaves'], 'hypothesizedRedMarkers': RED})


def objects(path):
    text = path.read_text()
    decoder = json.JSONDecoder()
    result = []
    while text.strip():
        text = text.lstrip()
        item, end = decoder.raw_decode(text)
        result.append(item)
        text = text[end:]
    return result


def normalized_graph(values, repo):
    require(len(values) == 365 and len({v['Path'] for v in values}) == 365, '365-module MVS missing/duplicate')
    require(sum(v.get('Main') is True for v in values) == 1, 'main module cardinality differs')
    require(sum(v['Path'] == MODULE for v in values) == 1, 'req module cardinality differs')
    normalized = copy.deepcopy(values)
    for item in normalized:
        if item.get('Main'):
            require('Replace' not in item, 'unexpected main replacement')
            for key, expected in (('Dir', repo / 'backend'), ('GoMod', repo / 'backend/go.mod')):
                require(Path(item[key]).resolve() == expected.resolve(), 'main checkout path differs')
                item[key] = '<SAME-FROZEN-CHECKOUT>/' + expected.relative_to(repo).as_posix()
        elif item['Path'] == MODULE:
            require(item['Version'] == 'v3.57.0', 'req original version differs')
            require('Replace' in item, 'local req replacement missing')
            for node in (item, item['Replace']):
                for key, expected in (('Dir', repo / PREFIX), ('GoMod', repo / PREFIX / 'go.mod')):
                    require(Path(node[key]).resolve() == expected.resolve(), 'req checkout path differs')
                    node[key] = '<SAME-FROZEN-CHECKOUT>/' + expected.relative_to(repo).as_posix()
        else:
            require(isinstance(item.get('Version'), str) and item['Version'], 'external module is not versioned')
            require('Replace' not in item, 'unexpected external replacement')
            if 'Dir' in item:
                require(isinstance(item['Dir'], str) and item['Dir'] and Path(item['Dir']).is_absolute(),
                        'external cache Dir must be a nonempty absolute path')
    return normalized


def graphs(baseline, candidate, evidence):
    cold = objects(evidence / 'baseline/modules.json')
    base = objects(evidence / 'baseline-modules-warm.json')
    cold_normalized = normalized_graph(cold, baseline)
    warm_normalized = normalized_graph(base, baseline)
    require([v['Path'] for v in cold] == [v['Path'] for v in base], 'cold/warm module path/order changed')
    cache_changes = []
    for cold_item, warm_item in zip(cold_normalized, warm_normalized):
        if not cold_item.get('Main') and cold_item['Path'] != MODULE:
            # Only optional cache Dir presence may differ in the original cold
            # snapshot. All other external fields remain exact, and two actual
            # Dir values may never move. Warm candidate comparison below retains
            # Dir itself and every other field without this exception.
            if 'Dir' in cold_item and 'Dir' in warm_item:
                require(cold_item['Dir'] == warm_item['Dir'], 'external cold/warm cache Dir moved')
            elif ('Dir' in cold_item) != ('Dir' in warm_item):
                cache_changes.append({'path': cold_item['Path'], 'coldDir': cold_item.get('Dir'),
                                      'warmDir': warm_item.get('Dir')})
            cold_other, warm_other = dict(cold_item), dict(warm_item)
            cold_other.pop('Dir', None)
            warm_other.pop('Dir', None)
            require(cold_other == warm_other, 'cold/warm external non-Dir MVS fields changed')
        else:
            require(cold_item == warm_item, 'cold/warm main or req MVS fields changed')
    before = objects(evidence / 'candidate-modules-before.json')
    after = objects(evidence / 'candidate-modules-after.json')
    require(warm_normalized == normalized_graph(before, candidate) == normalized_graph(after, candidate),
            'candidate MVS changed beyond proven checkout paths')
    baseline_packages = objects(evidence / 'baseline/packages.json')
    candidate_packages = objects(evidence / 'candidate-packages.json')
    require({p['ImportPath'] for p in baseline_packages} == {p['ImportPath'] for p in candidate_packages},
            'candidate req package selection differs')
    def selected(packages):
        return {(p['ImportPath'], n) for p in packages for n in p.get('TestGoFiles', []) + p.get('XTestGoFiles', [])}
    btests, ctests = selected(baseline_packages), selected(candidate_packages)
    require(not btests - ctests, 'original test selection lost')
    require(ctests - btests == {(MODULE, p.removeprefix(PREFIX)) for p in ADDED if p.endswith('_test.go')},
            'candidate test file selection differs')
    save(evidence, 'same-mvs-selection-proof.json', {'moduleCount': len(base),
         'normalization': 'warm baseline versus candidate: only exact main/req Dir and GoMod checkout paths; all other fields including external Dir and order unchanged',
         'coldToWarmOptionalExternalCacheDirChanges': cache_changes,
         'coldToWarmAllOtherFieldsIdentical': True,
         'graphsSHA256': {p: sha((evidence / p).read_bytes()) for p in (
             'baseline/modules.json', 'baseline-modules-warm.json', 'candidate-modules-before.json', 'candidate-modules-after.json')},
         'addedSelectedTests': sorted(ctests - btests), 'retainedSelectedTests': sorted(btests)})


def proof(candidate, evidence):
    provenance = json.loads((candidate / PREFIX / 'CLIENT-BEHAVIOR-PROVENANCE.json').read_text())
    required = set(provenance['newNamedLeaves'])
    original_inventory = json.loads((candidate / 'backend/tools/req-http2-security-inventory.json').read_text())
    original = {(original_inventory[key.removesuffix('Leaves') + 'Package'], n)
                for key, names in original_inventory.items()
                if key.endswith('Leaves') and key.removesuffix('Leaves') + 'Package' in original_inventory for n in names}
    require(len(original) == original_inventory['totalNamedLeaves'] == 401, 'original401 inventory differs')
    reports = {}
    for phase in ('f-named', 'f-all'):
        values = [json.loads(line) for line in (evidence / (phase + '.json')).read_text().splitlines()]
        require((evidence / (phase + '.exit')).read_text().strip() == '1', phase + ' must have actual assertion exit1')
        combined = (evidence / (phase + '.stderr')).read_text() + ''.join(v.get('Output', '') for v in values)
        require(not any(s in combined for s in ('WARNING: DATA RACE', 'panic:', '[build failed]',
                    'FAIL\tcommand-line-arguments', 'fatal error:', 'no Go files in', 'undefined:')),
                phase + ' fixture/build/race failure')
        runs = collections.Counter((v['Package'], v['Test']) for v in values if v['Action'] == 'run' and 'Test' in v)
        passes = collections.Counter((v['Package'], v['Test']) for v in values if v['Action'] == 'pass' and 'Test' in v)
        fails = collections.Counter((v['Package'], v['Test']) for v in values if v['Action'] == 'fail' and 'Test' in v)
        require(not any(v['Action'] == 'skip' and 'Test' in v for v in values), phase + ' skipped test')
        fruns = {n for (p, n) in runs if p == MODULE and n.startswith(ROOTS)}
        fleaves = {n for n in fruns if not any(other.startswith(n + '/') for other in fruns)}
        require(fleaves == required, phase + ' actual F44 leaves differ')
        allowed_failing_names = set(RED)
        for name in RED:
            parts = name.split('/')
            allowed_failing_names.update('/'.join(parts[:i]) for i in range(1, len(parts)))
        require(set(fails) == {(MODULE, n) for n in allowed_failing_names}, phase + ' unexpected/missing test failure')
        leaf_report = []
        for name in sorted(required):
            key = MODULE, name
            require(runs[key] == 1, phase + ' missing/duplicate F RUN: ' + name)
            expected_action = 'fail' if name in RED else 'pass'
            require((fails if name in RED else passes)[key] == 1 and (passes if name in RED else fails)[key] == 0,
                    phase + ' unexpected F leaf result: ' + name)
            output = ''.join(v.get('Output', '') for v in values if v.get('Package') == MODULE and v.get('Test') == name)
            if name in RED:
                require(RED[name] in output, phase + ' failure lacks completed fixture assertion marker: ' + name)
                require(sum(RED[name] in v.get('Output', '') for v in values
                            if v.get('Package') == MODULE and v.get('Test') == name) == 1,
                        phase + ' failure assertion marker not exactly once: ' + name)
            leaf_report.append({'test': name, 'actualAction': expected_action, 'marker': RED.get(name)})
        package_fails = collections.Counter(v['Package'] for v in values if v['Action'] == 'fail' and 'Test' not in v)
        require(dict(package_fails) == {MODULE: 1}, phase + ' unexpected/missing package failure')
        if phase == 'f-all':
            require(all(runs[k] == passes[k] == 1 and not fails[k] for k in original), 'candidate lost original401 actual PASS')
            packages = objects(evidence / 'candidate-packages.json')
            expected_pass = {p['ImportPath'] for p in packages if p.get('TestGoFiles') or p.get('XTestGoFiles')} - {MODULE}
            actual_pass = collections.Counter(v['Package'] for v in values if v['Action'] == 'pass' and 'Test' not in v)
            require(dict(actual_pass) == {p: 1 for p in expected_pass}, 'candidate complete test package results differ')
            no_test = {p['ImportPath'] for p in packages if not (p.get('TestGoFiles') or p.get('XTestGoFiles'))}
            skip = collections.Counter(v['Package'] for v in values if v['Action'] == 'skip' and 'Test' not in v)
            require(dict(skip) == {p: 1 for p in no_test}, 'no-test package SKIP classification differs')
        reports[phase] = {'requiredFLeaves': 44, 'actualRedLeaves': len(RED), 'actualPassingFLeaves': 44 - len(RED),
                          'leafResults': leaf_report, 'packageFailures': dict(package_fails),
                          'actualExit': 1, 'original401Passed': phase == 'f-all'}
    save(evidence, 'actual-red-proof.json', reports)


def finish(baseline, candidate, evidence):
    require((evidence / 'baseline.exit').read_text().strip() == '0', 'unchanged original401 CI failed')
    for phase, repo, head, tree in (
            ('baseline', baseline, BASE, BASE_TREE), ('candidate', candidate, CANDIDATE, CANDIDATE_TREE)):
        current = freeze(repo, head, tree)
        require(current == json.loads((evidence / (phase + '-frozen.json')).read_text()), 'full frozen Git inputs changed')
        save(evidence, phase + '-after.json', current)
        ignored = {name.decode() for name in git(repo, 'ls-files', '--others', '--ignored',
                   '--exclude-standard', '-z').split(b'\0') if name}
        expected_ignored = {'backend/coverage-req-http2.out'} if phase == 'baseline' else set()
        require(ignored == expected_ignored, phase + ' unexpected ignored artifact/input after execution')
        if phase == 'baseline':
            require((repo / 'backend/coverage-req-http2.out').read_bytes() ==
                    (evidence / 'baseline/all.mapped.out').read_bytes(),
                    'baseline generated coverage differs from original mapped profile')
        save(evidence, phase + '-ignored-after.json', {'paths': sorted(ignored),
             'baselineCoverageByteexactOriginalMappedProfile': phase == 'baseline'})
    graphs(baseline, candidate, evidence)
    proof(candidate, evidence)
    (evidence / 'complete.txt').write_text('All five genuine RED leaves have exact intended assertions; other39 F leaves and original401 PASS. No production fix or approval.\n')


if __name__ == '__main__':
    operation, baseline, candidate, evidence = sys.argv[1:]
    args = tuple(map(lambda p: Path(p).resolve(), (baseline, candidate, evidence)))
    if operation == 'setup':
        setup(*args)
    elif operation == 'finish':
        finish(*args)
    else:
        raise SystemExit('unknown remote operation')
