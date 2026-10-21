#!/usr/bin/env python3
"""Remote CI only. Preserve genuine OLD failures; reuse separately audited existing NEW execution."""
import collections
import copy
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tarfile

OLD = '7b0d92fb4c6bd52a62218d505130944163b73464'
OLD_TREE = 'c34bdcd39f97ea0623ff0aad9cfcbf04b04d7612'
NEW = 'beed0eb87da3eeb8f88ca7706d0e54a6cb6452d2'
NEW_TREE = 'c15e6475533244ae0736c6ed34700223a3d9b02f'
PRIOR_PROOF = '510095bfe25f5db976e9c1315d810e19d560f664'
FROZEN_F = '9c0c0641aa6a90accd4c688c5bcad7eebc53e699'
PREFIX = 'backend/third_party/req/'
MODULE = 'github.com/imroc/req/v3'
FIXTURES = ('client_behavior_peer_test.go', 'client_behavior_observer_test.go',
            'client_behavior_contract_test.go', 'client_redirect_cookie_contract_test.go',
            'client_response_contract_test.go', 'digest_ownership_peer_test.go',
            'digest_ownership_contract_test.go')
CONTROL = {'.github/ci/digest1743-behavior.py', '.github/workflows/digest1743-behavior.yml'}
PRIOR_CONTROL = {'.github/ci/req-client-f-red-proof.py', '.github/workflows/req-client-f-red-proof.yml'}
SCRIPT = 'backend/tools/req-http2-security-ci.sh'
EXISTING_NEW = {
    'run': 39385754409, 'actualHead': '7c766604fbf3fa34a5b52e393f3a971d6d9ec976',
    'actualTree': 'de9493876ce1f7df133a3a537bda8c49076d5e37',
    'equivalentApplicationHead': NEW, 'equivalentApplicationTree': NEW_TREE,
    'artifact': 12247297298, 'artifactMembers': 158,
    'artifactSHA256': 'f0fd78c4bb86aa3b0290b1e58d1dc4a83c875fc05685c4be2496f2fe30d9371d',
    'rootActualReceiptSHA256': 'e19113fa098dba353e0b86fe9d476f880a75bb327985fad1c3e2a6eab02a3cd0',
    'actualNamedRUNandPASS': 548, 'actualFullRUNandPASS': 691,
    'mandatoryLeavesPerPhase': 468, 'digestAddedBlockStatements': [28, 28],
    'claimLimit': 'Existing actual execution was at 7c766, whose entire application equals beed plus two formatter controls. This controller does not reexecute NEW. Independent actual review and formal aggregate85 are separate requirements.'}
FORMAT_CONTROL = {'.github/ci/digest1743-format.py', '.github/workflows/req-client-f-formatter.yml'}

def git(root, *args):
    return subprocess.check_output(['git', '-C', str(root), *args])


def sha(data):
    return hashlib.sha256(data).hexdigest()


def save(path, data):
    path.write_text(json.dumps(data, indent=2, sort_keys=True) + '\n')


def snapshot(root, head, tree, output, allowed_coverage=None):
    actual_head = git(root, 'rev-parse', 'HEAD').decode().strip()
    actual_tree = git(root, 'rev-parse', 'HEAD^{tree}').decode().strip()
    assert actual_head == head and (not tree or actual_tree == tree), 'unexpected Git head/tree'
    status = git(root, 'status', '--porcelain=v1', '--untracked-files=all')
    Path(str(output) + '.status').write_bytes(status)
    assert not status, 'dirty tracked/untracked input'
    ignored = git(root, 'ls-files', '--others', '--ignored', '--exclude-standard', '-z')
    Path(str(output) + '.ignored').write_bytes(ignored)
    expected_ignored = b'backend/coverage-req-http2.out\0' if allowed_coverage is not None else b''
    assert ignored == expected_ignored, 'unexpected ignored checkout input/artifact appeared'
    if allowed_coverage is not None:
        assert (root / 'backend/coverage-req-http2.out').read_bytes() == allowed_coverage.read_bytes(), 'generated coverage differs from mapped original'
    index = git(root, 'ls-files', '--stage', '-z')
    Path(str(output) + '.index').write_bytes(index)
    files = []
    for row in index.split(b'\0'):
        if not row:
            continue
        meta, rawname = row.split(b'\t', 1)
        mode, blob, stage = meta.decode().split()
        assert stage == '0' and mode in ('100644', '100755', '120000')
        name = os.fsdecode(rawname)
        path = root / name
        info = path.lstat()
        if mode == '120000':
            assert stat.S_ISLNK(info.st_mode)
            raw = os.fsencode(os.readlink(path))
        else:
            assert stat.S_ISREG(info.st_mode) and not path.is_symlink()
            assert bool(info.st_mode & 0o111) == (mode == '100755')
            raw = path.read_bytes()
        assert hashlib.sha1(b'blob ' + str(len(raw)).encode() + b'\0' + raw).hexdigest() == blob, name
        files.append({'path': name, 'mode': mode, 'blob': blob, 'bytes': len(raw), 'sha256': sha(raw)})
    assert any(x['path'].startswith('frontend/') for x in files), 'full shared fixture repository missing'
    result = {'head': actual_head, 'tree': actual_tree, 'files': files}
    save(output, result)
    return result


def archive(root, evidence, label, frozen):
    path = evidence / (label + '-full-input.tar')
    with path.open('wb') as stream:
        subprocess.run(['git', '-C', str(root), 'archive', '--format=tar', 'HEAD'], stdout=stream, check=True)
    expected = {x['path']: x for x in frozen['files']}
    with tarfile.open(path, 'r:') as package:
        members = [x for x in package.getmembers() if not x.isdir()]
        assert len(members) == len(expected) and {x.name for x in members} == set(expected)
        for item in members:
            frozen_item = expected[item.name]
            if frozen_item['mode'] == '120000':
                assert item.issym()
                data = os.fsencode(item.linkname)
            else:
                assert item.isfile() and bool(item.mode & 0o111) == (frozen_item['mode'] == '100755')
                data = package.extractfile(item).read()
            assert len(data) == frozen_item['bytes'] and sha(data) == frozen_item['sha256']
    return {'bytes': path.stat().st_size, 'sha256': sha(path.read_bytes()), 'trackedFiles': len(expected)}

def prepare(controller, old, new, evidence):
    assert evidence.is_dir() and not list(evidence.iterdir()), 'fresh external evidence directory required'
    assert all(not evidence.is_relative_to(root) for root in (controller, old, new))
    roots = {'controller': controller, 'old': old, 'new': new}
    expected = {'controller': (os.environ['GITHUB_SHA'], ''), 'old': (OLD, OLD_TREE), 'new': (NEW, NEW_TREE)}
    snapshots, archives = {}, {}
    for label, root in roots.items():
        snapshots[label] = snapshot(root, *expected[label], evidence / (label + '-before.json'))
        archives[label] = archive(root, evidence, label, snapshots[label])
    assert git(controller, 'rev-list', '--parents', '-n', '1', 'HEAD').decode().split()[1:] == [PRIOR_PROOF, NEW], 'proof parents differ'
    new_files = {v['path']: v for v in snapshots['new']['files']}
    control_files = {v['path']: v for v in snapshots['controller']['files']}
    assert all(control_files.get(path) == item for path, item in new_files.items())
    assert set(control_files) - set(new_files) == CONTROL | PRIOR_CONTROL
    for name in PRIOR_CONTROL:
        assert (controller / name).read_bytes() == git(controller, 'show', PRIOR_PROOF + ':' + name), 'prior failed-proof history changed'
    # Bind the separately captured NEW execution to the exact application Git
    # inputs; cite its real controller head, never pretend beed was executed.
    assert git(controller, 'rev-parse', EXISTING_NEW['actualHead'] + '^{tree}').decode().strip() == EXISTING_NEW['actualTree']
    executed_tree = {}
    for row in git(controller, 'ls-tree', '-r', '-z', EXISTING_NEW['actualHead']).split(b'\0'):
        if row:
            meta, rawname = row.split(b'\t', 1)
            mode, kind, blob = meta.decode().split()
            assert kind == 'blob'
            executed_tree[os.fsdecode(rawname)] = {'mode': mode, 'blob': blob}
    assert all(executed_tree.get(path) == {'mode': item['mode'], 'blob': item['blob']} for path, item in new_files.items())
    assert set(executed_tree) - set(new_files) == FORMAT_CONTROL and len(executed_tree) == 4494
    save(evidence / 'existing-NEW-execution-reference.json', EXISTING_NEW)
    assert git(new, 'rev-parse', 'HEAD^').decode().strip() == OLD
    old_manifest = json.loads((old / PREFIX / 'UPSTREAM-MANIFEST.json').read_text())
    new_manifest = json.loads((new / PREFIX / 'UPSTREAM-MANIFEST.json').read_text())
    assert old_manifest['originalFiles'] == new_manifest['originalFiles'] and len(old_manifest['originalFiles']) == 132
    originals = old_manifest['originalFiles']
    assert sum(v['path'].endswith('_test.go') for v in originals) == 14
    assert sum(v['path'].endswith('.go') and not v['path'].endswith('_test.go') for v in originals) == 89
    assert new_manifest['adaptedCandidateFiles'][:-1] == old_manifest['adaptedCandidateFiles']
    assert len(old_manifest['adaptedCandidateFiles']) == 11 and len(new_manifest['adaptedCandidateFiles']) == 12
    assert old_manifest['originalUnmodifiedFileCount'] == 121 and new_manifest['originalUnmodifiedFileCount'] == 120
    assert old_manifest['newOwnedFiles'] == new_manifest['newOwnedFiles'] and len(new_manifest['newOwnedFiles']) == 41
    inventory_path = 'backend/tools/req-http2-security-inventory.json'
    assert (old / inventory_path).read_bytes() == (new / inventory_path).read_bytes()
    inventory = json.loads((new / inventory_path).read_text())
    assert inventory['totalNamedLeaves'] == 468 and inventory['clientBehaviorCount'] == 44 and inventory['digestOwnershipCount'] == 23
    assert sum(v for k, v in inventory.items() if k.endswith('Count') and k not in ('clientBehaviorCount', 'digestOwnershipCount')) == 401
    fixture_records = []
    for name in FIXTURES:
        raw = (old / PREFIX / name).read_bytes()
        assert raw == (new / PREFIX / name).read_bytes(), 'OLD/NEW fixture differs'
        if name.startswith('client_'):
            assert raw == git(new, 'show', FROZEN_F + ':' + PREFIX + name), 'F44 fixture changed'
        fixture_records.append({'path': PREFIX + name, 'bytes': len(raw), 'sha256': sha(raw)})
    assert git(old, 'rev-parse', 'HEAD:' + PREFIX + 'digest.go').decode().strip() == '0695d8c3063c448e29ab05d44f0f8fbbd9a1cf81'
    save(evidence / 'same-fixtures-and-scope.json', {'fixtures': fixture_records, 'mandatoryLeaves': 468,
         'originalLeaves': 401, 'FLeaves': 44, 'ownershipLeaves': 23, 'originalFiles': 132,
         'productionFiles': 89, 'originalTests': 14, 'oldAdaptations': 11, 'newAdaptations': 12,
         'claimLimit': 'Source identity only. OLD23 actual dispositions remain unknown; aggregate85 gates remain HOLD.'})
    save(evidence / 'complete-input-archives.json', archives)
    for name in CONTROL:
        shutil.copyfile(controller / name, evidence / ('executed-controller.py' if name.endswith('.py') else 'executed-workflow.yml'))
    (evidence / 'prepared.txt').write_text('Exact full controller/OLD/NEW inputs sealed. No test result yet.\n')


def invoke(command, cwd, stem, env=None):
    with Path(str(stem) + '.stdout').open('wb') as out, Path(str(stem) + '.stderr').open('wb') as err:
        result = subprocess.run(command, cwd=cwd, env=env, stdout=out, stderr=err)
    Path(str(stem) + '.exit').write_text(str(result.returncode) + '\n')
    return result.returncode


def old_capture_script(old, evidence):
    # Derive only an external execution wrapper. The pinned application script
    # and all its tools stay byteexact. Foundation and auxiliary gates retain
    # their original source bytes; named/all PASS checkers cannot accept OLD.
    raw = (old / SCRIPT).read_text()
    start = raw.index('go test -p=1 -parallel=1 -race -count=1 -timeout=10m -json -run ')
    named_end = raw.index('\npython3 "$checker" named "$backend" "$evidence"', start)
    full_start = raw.index('go test -p=1 -parallel=1 -race -count=1 -timeout=15m -json ', named_end)
    full_end = raw.index('\npython3 "$checker" all "$backend" "$evidence"', full_start)
    suffix_start = raw.index('python3 "$checker" original "$backend" "$evidence"', full_end)
    assert raw.count('go test -p=1 -parallel=1 -race -count=1 -timeout=10m -json -run ') == 1
    assert raw.count('go test -p=1 -parallel=1 -race -count=1 -timeout=15m -json ') == 1
    assert raw[named_end:full_start] == '\npython3 "$checker" named "$backend" "$evidence"\n# Wildcard deliberately omits the original internal/testdata helper suite.\n# Its cert_test.go lacks an io import; retain both original files verbatim.\n'
    assert raw[full_end:suffix_start] == '\npython3 "$checker" all "$backend" "$evidence"\n'
    named, full = raw[start:named_end], raw[full_start:full_end]
    captured = '\n'.join(('printf \'foundation completed\\n\' > \"$evidence/foundation-completed.txt\"', 'set +e', named, 'named_status=$?',
        'printf \'%s\\n\' "$named_status" > "$evidence/named.exit"', full, 'all_status=$?',
        'printf \'%s\\n\' "$all_status" > "$evidence/all.exit"', 'set -e',
        'printf \'both actual Go commands returned\\n\' > "$evidence/both-suites-captured.txt"', ''))
    # The following original gates may independently fail (including coverage).
    # Never fabricate their success. Raw test captures already exist beforehand.
    derived = raw[:start] + captured + raw[suffix_start:]
    derived += '\nprintf \'unchanged auxiliary gates completed\\n\' > \"$evidence/auxiliary-gates-completed.txt\"\n'
    derived += '# Preserve actual OLD failure after the unchanged auxiliary gates.\n'
    derived += 'test "$named_status" -eq 0 && test "$all_status" -eq 0\n'
    path = evidence / 'OLD-capture-wrapper.sh'
    path.write_text(derived)
    save(evidence / 'OLD-wrapper-derivation.json', {'sourceScriptSHA256': sha(raw.encode()),
         'wrapperSHA256': sha(derived.encode()), 'foundationBytes': len(raw[:start].encode()),
         'unchangedAuxiliaryBytes': len(raw[suffix_start:].encode()),
         'namedCommandSHA256': sha(named.encode()), 'fullCommandSHA256': sha(full.encode()),
         'removedPASSOnlyCheckers': ['named', 'all'],
         'policy': 'Both actual Go commands captured regardless of either result; actual nonzero remains nonzero. No expected-RED allowlist.'})
    return path


def objects(path):
    decoder, raw, values = json.JSONDecoder(), path.read_text(), []
    while raw.strip():
        value, end = decoder.raw_decode(raw.lstrip())
        values.append(value)
        raw = raw.lstrip()[end:]
    return values


def graph(values, repo):
    assert len(values) == 365 and len({v['Path'] for v in values}) == 365
    assert sum(v.get('Main') is True for v in values) == 1
    assert sum(v['Path'] == MODULE for v in values) == 1
    result = copy.deepcopy(values)
    for value in result:
        if value.get('Main'):
            assert 'Replace' not in value
            for key, target in (('Dir', repo / 'backend'), ('GoMod', repo / 'backend/go.mod')):
                assert Path(value[key]).resolve() == target.resolve()
                value[key] = '<CHECKOUT>/' + target.relative_to(repo).as_posix()
        elif value['Path'] == MODULE:
            assert value['Version'] == 'v3.57.0' and value['Replace']['Path'] == './third_party/req'
            for node in (value, value['Replace']):
                for key, target in (('Dir', repo / PREFIX), ('GoMod', repo / PREFIX / 'go.mod')):
                    assert Path(node[key]).resolve() == target.resolve()
                    node[key] = '<CHECKOUT>/' + target.relative_to(repo).as_posix()
        else:
            assert isinstance(value.get('Version'), str) and value['Version'] and 'Replace' not in value
            if 'Dir' in value:
                assert isinstance(value['Dir'], str) and value['Dir'] and Path(value['Dir']).is_absolute()
    return result


def compare_cold_warm(cold, warm):
    assert [v['Path'] for v in cold] == [v['Path'] for v in warm]
    changes = []
    for before, after in zip(cold, warm):
        if not before.get('Main') and before['Path'] != MODULE:
            if 'Dir' in before and 'Dir' in after:
                assert before['Dir'] == after['Dir'], 'external cache Dir moved'
            elif ('Dir' in before) != ('Dir' in after):
                changes.append({'path': before['Path'], 'coldDir': before.get('Dir'), 'warmDir': after.get('Dir')})
            left, right = dict(before), dict(after)
            left.pop('Dir', None)
            right.pop('Dir', None)
            assert left == right, 'external non-Dir module field changed'
        else:
            assert before == after, 'main/req module fields changed'
    return changes


def execute(old, new, evidence):
    assert (evidence / 'prepared.txt').is_file()
    for label, root in (('old', old), ('new', new)):
        assert invoke(['go', 'list', '-m', '-json', 'all'], root / 'backend', evidence / (label + '-modules-before')) == 0
    env = dict(os.environ, REQ_SECURITY_EVIDENCE=str(evidence / 'actual'),
               REQ_SECURITY_EXPECTED_HEAD=OLD, REQ_SECURITY_GOMAXPROCS='2')
    source = old_capture_script(old, evidence)
    status = invoke(['bash', str(source)], old / 'backend', evidence / 'actual-script', env)
    raise SystemExit(status)


def actual_events(root, evidence):
    actual = evidence / 'actual'
    inventory = json.loads((root / 'backend/tools/req-http2-security-inventory.json').read_text())
    kinds = [key[:-6] for key in inventory if key.endswith('Leaves') and key[:-6] + 'Package' in inventory]
    required = {(inventory[kind + 'Package'], name): kind for kind in kinds for name in inventory[kind + 'Leaves']}
    assert len(required) == 468
    report = {'phase': 'old', 'suites': {}, 'errors': [], 'unknownOLD23': 'No predicted failure allowlist; inspect actual output and independently classify every failure.'}
    for suite in ('named', 'all'):
        try:
            values = [json.loads(line) for line in (actual / (suite + '.json')).read_text().splitlines()]
            status = int((actual / (suite + '.exit')).read_text().strip())
            combined = (actual / (suite + '.stderr')).read_text() + ''.join(v.get('Output', '') for v in values)
            assert not any(s in combined for s in ('WARNING: DATA RACE', 'panic:', '[build failed]', 'fatal error:', 'undefined:', 'no Go files in')), 'race/compile/panic fixture blocker'
            runs = collections.Counter((v['Package'], v['Test']) for v in values if v['Action'] == 'run' and 'Test' in v)
            terminals = collections.defaultdict(list)
            for value in values:
                if value['Action'] in ('pass', 'fail', 'skip') and 'Test' in value:
                    terminals[(value['Package'], value['Test'])].append(value['Action'])
            leaves = []
            for key, kind in required.items():
                actions = terminals[key]
                output = ''.join(v.get('Output', '') for v in values if (v.get('Package'), v.get('Test')) == key)
                leaves.append({'package': key[0], 'test': key[1], 'kind': kind, 'runs': runs[key], 'actualTerminals': actions, 'output': output})
            # Save every observed mandatory leaf before judging any assertion:
            # one invalid OLD fixture must not hide another case's raw outcome.
            save(evidence / (suite + '-all468-actual-leaves.json'), leaves)
            for item in leaves:
                assert item['runs'] == 1 and len(item['actualTerminals']) == 1 and item['actualTerminals'][0] in ('pass', 'fail'), 'missing/duplicate/skipped mandatory leaf: ' + item['test']
                if item['kind'] not in ('clientBehavior', 'digestOwnership'):
                    assert item['actualTerminals'] == ['pass'], 'mandatory retained leaf did not PASS: ' + item['test']
            assert not any(v['Action'] == 'skip' and 'Test' in v for v in values), 'test-level skip'
            # OLD failures outside F44/new23 (or their parent names) are blockers.
            failures = {key for key, actions in terminals.items() if 'fail' in actions}
            relevant = {key for key, kind in required.items() if kind in ('clientBehavior', 'digestOwnership')}
            allowed_ancestry = set(relevant)
            for package, name in relevant:
                parts = name.split('/')
                allowed_ancestry.update((package, '/'.join(parts[:i])) for i in range(1, len(parts)))
            assert failures <= allowed_ancestry, 'unexpected ordinary-suite failure outside observed scope'
            if suite == 'all':
                listed = objects(actual / 'packages.json')
                selected = {v['ImportPath']: v for v in listed}
                assert len(listed) == len(selected) == 23
                package_events = [v for v in values if 'Test' not in v and v['Action'] in ('pass', 'fail', 'skip')]
                counts = collections.Counter(v['Package'] for v in package_events)
                assert set(counts) == set(selected) and all(v == 1 for v in counts.values()), 'incomplete/duplicate23 package terminals'
                packages = {v['Package']: v['Action'] for v in package_events}
                test_bearing = {p for p, item in selected.items() if item.get('TestGoFiles') or item.get('XTestGoFiles')}
                assert all(packages[p] == 'pass' for p in test_bearing - {MODULE})
                assert all(packages[p] != 'fail' for p in set(selected) - test_bearing)
            else:
                package_events = [v for v in values if 'Test' not in v and v['Action'] in ('pass', 'fail', 'skip')]
                counts = collections.Counter(v['Package'] for v in package_events)
                expected_packages = {key[0] for key in required}
                assert len(expected_packages) == 5 and set(counts) == expected_packages and all(v == 1 for v in counts.values()), 'incomplete/duplicate named package terminals'
                packages = {v['Package']: v['Action'] for v in package_events}
                assert all(action == 'pass' for name, action in packages.items() if name != MODULE)
                test_bearing = expected_packages
            assert (status == 0) == (not any(v['Action'] == 'fail' for v in values)), 'actual Go exit disagrees with fail events'
            report['suites'][suite] = {'actualGoExit': status, 'mandatoryLeaves': leaves,
                 'mandatoryPassCount': sum(v['actualTerminals'] == ['pass'] for v in leaves),
                 'mandatoryFailCount': sum(v['actualTerminals'] == ['fail'] for v in leaves),
                 'actualPackageTerminals': packages, 'testBearingPackages': sorted(test_bearing),
                 'claimLimit': 'No-test coverage package PASS is not testcase execution.'}
        except (OSError, ValueError, KeyError, AssertionError) as error:
            report['errors'].append(suite + ': ' + str(error))
    save(evidence / 'actual-leaf-observations.json', report)
    assert not report['errors'], report['errors']


def finish(controller, old, new, evidence):
    errors = []
    roots = {'controller': controller, 'old': old, 'new': new}
    expected = {'controller': (os.environ['GITHUB_SHA'], ''), 'old': (OLD, OLD_TREE), 'new': (NEW, NEW_TREE)}
    try:
        graphs, changes = {}, {}
        for label, root in (('old', old), ('new', new)):
            assert invoke(['go', 'list', '-m', '-json', 'all'], root / 'backend', evidence / (label + '-modules-after')) == 0
            graphs[label] = graph(objects(evidence / (label + '-modules-after.stdout')), root)
            cold = graph(objects(evidence / (label + '-modules-before.stdout')), root)
            changes[label] = compare_cold_warm(cold, graphs[label])
        assert graphs['old'] == graphs['new'], 'warm OLD/NEW365 full MVS differs beyond proven checkout paths'
        save(evidence / 'same365-MVS-proof.json', {'identicalModules': 365, 'coldWarmOptionalExternalDirChanges': changes,
             'warmAllOtherFieldsAndOrderIdentical': True, 'normalization': 'Only proven main/req Dir and GoMod checkout paths; no external field removed from warm comparison.'})
    except (OSError, ValueError, KeyError, AssertionError, subprocess.SubprocessError) as error:
        errors.append('MVS: ' + str(error))
    for label, root in roots.items():
        try:
            generated = root / 'backend/coverage-req-http2.out'
            coverage = evidence / 'actual/all.mapped.out' if label == 'old' and generated.exists() else None
            current = snapshot(root, *expected[label], evidence / (label + '-after.json'), coverage)
            assert current == json.loads((evidence / (label + '-before.json')).read_text())
        except (OSError, ValueError, KeyError, AssertionError, subprocess.SubprocessError) as error:
            errors.append(label + ' source after: ' + str(error))
    try:
        actual_events(old, evidence)
    except (OSError, ValueError, KeyError, AssertionError) as error:
        errors.append('actual event audit: ' + str(error))
    try:
        archives = json.loads((evidence / 'complete-input-archives.json').read_text())
        for label, item in archives.items():
            raw = (evidence / (label + '-full-input.tar')).read_bytes()
            assert len(raw) == item['bytes'] and sha(raw) == item['sha256']
        assert set(archives) == {'controller', 'old', 'new'}
        assert (evidence / 'go-version.txt').read_text().strip() == 'go version go1.26.9 linux/amd64'
        actual_status = int((evidence / 'actual-script.exit').read_text().strip())
        assert (evidence / 'actual/foundation-completed.txt').is_file(), 'OLD foundation incomplete'
        assert (evidence / 'actual/both-suites-captured.txt').is_file(), 'OLD full capture incomplete'
        assert (evidence / 'actual/auxiliary-gates-completed.txt').is_file(), 'OLD original auxiliary gates failed/incomplete'
        save(evidence / 'actual-status-reference.json', {'wrapperActualExit': actual_status,
             'namedActualGoExit': int((evidence / 'actual/named.exit').read_text()),
             'fullActualGoExit': int((evidence / 'actual/all.exit').read_text()),
             'existingNEW': EXISTING_NEW})
        # Presence is not a substitute for each real tool result. Retain all
        # underlying reports; OLD wrapper/script actual nonzero remains failure.
        for name in ('package-selection-proof.json', 'original-source-proof.json', 'old-graph-proof.json',
                     'old-red-proof.json', 'coverage-path-only-proof.json', 'official-and-local-adapted-fix-block-coverage.json'):
            assert (evidence / 'actual' / name).is_file(), 'required evidence incomplete: ' + name
    except (OSError, ValueError, KeyError, AssertionError) as error:
        errors.append('full original gates: ' + str(error))
    save(evidence / 'controller-final-review.json', {'phase': 'old', 'oldHead': OLD, 'newReference': EXISTING_NEW,
         'controllerHead': os.environ['GITHUB_SHA'], 'errors': errors,
         'scope': 'Exact OLD observation and separately recorded existing NEW scope only; Codecov85 and complete-Git85 remain HOLD. No PR approval.'})
    assert not errors, errors
    (evidence / 'controller-audit-complete.txt').write_text('Full input and observed scoped evidence audited. OLD job retains its real failed test status. Aggregate85 still HOLD.\n')


if __name__ == '__main__':
    operation = sys.argv[1]
    if operation == 'prepare':
        prepare(*(Path(x).resolve() for x in sys.argv[2:]))
    elif operation == 'execute':
        execute(*(Path(x).resolve() for x in sys.argv[2:]))
    elif operation == 'finish':
        finish(*(Path(x).resolve() for x in sys.argv[2:]))
    else:
        raise SystemExit('unknown remote operation')
