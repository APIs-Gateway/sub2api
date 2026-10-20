#!/usr/bin/env python3
"""Remote formatter evidence only; never executes application code or tests."""
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
FROZEN_F = '9c0c0641aa6a90accd4c688c5bcad7eebc53e699'
PREFIX = 'backend/third_party/req/'
FIXTURES = ('digest_ownership_peer_test.go', 'digest_ownership_contract_test.go')
FORMAT = FIXTURES + ('digest.go',)
F_FILES = ('client_behavior_peer_test.go', 'client_behavior_observer_test.go',
           'client_behavior_contract_test.go', 'client_redirect_cookie_contract_test.go',
           'client_response_contract_test.go')
CONTROL = {'.github/workflows/req-client-f-formatter.yml', '.github/ci/digest1743-format.py'}


def git(root, *args):
    return subprocess.check_output(['git', '-C', str(root), *args])


def sha(data):
    return hashlib.sha256(data).hexdigest()


def save(path, data):
    path.write_text(json.dumps(data, indent=2, sort_keys=True) + '\n')


def snapshot(root, head, tree, output):
    actual_head = git(root, 'rev-parse', 'HEAD').decode().strip()
    actual_tree = git(root, 'rev-parse', 'HEAD^{tree}').decode().strip()
    assert actual_head == head and (not tree or actual_tree == tree), 'unexpected Git head/tree'
    status = git(root, 'status', '--porcelain=v1', '--untracked-files=all')
    Path(str(output) + '.status').write_bytes(status)
    assert not status, 'dirty tracked/untracked input'
    ignored = git(root, 'ls-files', '--others', '--ignored', '--exclude-standard', '-z')
    Path(str(output) + '.ignored').write_bytes(ignored)
    assert not ignored, 'ignored checkout input/artifact appeared'
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
    roots = {'controller': controller, 'old': old, 'new': new}
    expected = {'controller': (os.environ['GITHUB_SHA'], ''), 'old': (OLD, OLD_TREE), 'new': (NEW, NEW_TREE)}
    snapshots, archives = {}, {}
    for label, root in roots.items():
        snapshots[label] = snapshot(root, *expected[label], evidence / (label + '-before.json'))
        archives[label] = archive(root, evidence, label, snapshots[label])
    assert subprocess.run(['git', '-C', str(controller), 'merge-base', '--is-ancestor', NEW, os.environ['GITHUB_SHA']]).returncode == 0
    assert subprocess.run(['git', '-C', str(new), 'merge-base', '--is-ancestor', OLD, NEW]).returncode == 0
    new_files = {x['path']: x for x in snapshots['new']['files']}
    controller_files = {x['path']: x for x in snapshots['controller']['files']}
    assert all(controller_files.get(path) == item for path, item in new_files.items())
    assert set(controller_files) - set(new_files) == CONTROL, 'controller changed/omitted application source'
    old_ledger = json.loads((old / PREFIX / 'UPSTREAM-MANIFEST.json').read_text())
    new_ledger = json.loads((new / PREFIX / 'UPSTREAM-MANIFEST.json').read_text())
    assert old_ledger['originalFiles'] == new_ledger['originalFiles'] and len(old_ledger['originalFiles']) == 132
    assert new_ledger['adaptedCandidateFiles'][:-1] == old_ledger['adaptedCandidateFiles']
    assert len(old_ledger['adaptedCandidateFiles']) == 11 and len(new_ledger['adaptedCandidateFiles']) == 12
    assert old_ledger['originalUnmodifiedFileCount'] == 121 and new_ledger['originalUnmodifiedFileCount'] == 120
    assert old_ledger['newOwnedFiles'] == new_ledger['newOwnedFiles'] and len(new_ledger['newOwnedFiles']) == 41
    assert git(old, 'rev-parse', 'HEAD:' + PREFIX + 'digest.go').decode().strip() == '0695d8c3063c448e29ab05d44f0f8fbbd9a1cf81'
    assert (old / 'backend/tools/req-http2-security-inventory.json').read_bytes() == (new / 'backend/tools/req-http2-security-inventory.json').read_bytes()
    inventory = json.loads((new / 'backend/tools/req-http2-security-inventory.json').read_text())
    assert inventory['totalNamedLeaves'] == 468 and inventory['clientBehaviorCount'] == 44 and inventory['digestOwnershipCount'] == 23
    inputs = []
    for label, root in (('old', old), ('new', new)):
        for name in FORMAT + F_FILES:
            relative = PREFIX + name
            data = (root / relative).read_bytes()
            raw = evidence / 'raw' / label / relative
            raw.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(root / relative, raw)
            assert raw.read_bytes() == data
            if name in FORMAT:
                target = evidence / 'formatted' / label / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(root / relative, target)
                assert target.read_bytes() == data
            else:
                assert data == git(root, 'show', FROZEN_F + ':' + relative), 'reviewed F5 input changed'
            inputs.append({'phase': label, 'path': relative, 'purpose': 'format-copy' if name in FORMAT else 'F-list-only',
                           'bytes': len(data), 'sha256': sha(data)})
    for name in FIXTURES + F_FILES:
        assert (old / PREFIX / name).read_bytes() == (new / PREFIX / name).read_bytes(), 'OLD/NEW fixture differs'
    save(evidence / 'inputs.json', inputs)
    save(evidence / 'complete-input-archives.json', archives)
    for name in CONTROL:
        shutil.copyfile(controller / name, evidence / ('executed-controller.py' if name.endswith('.py') else 'executed-workflow.yml'))


def judge(evidence):
    errors, results, archives = [], [], {}
    try:
        archives = json.loads((evidence / 'complete-input-archives.json').read_text())
        assert set(archives) == {'old', 'new', 'controller'}
        for label, item in archives.items():
            data = (evidence / (label + '-full-input.tar')).read_bytes()
            assert len(data) == item['bytes'] and sha(data) == item['sha256']
    except (OSError, ValueError, KeyError, AssertionError) as error:
        errors.append('frozen complete archives incomplete/changed: ' + str(error))
    for label in ('old', 'new', 'controller'):
        try:
            assert (evidence / (label + '-after.exit')).read_text().strip() == '0'
            assert (evidence / (label + '-after.stdout')).read_bytes() == b''
            assert (evidence / (label + '-after.stderr')).read_bytes() == b''
            assert (evidence / (label + '-before.json')).read_bytes() == (evidence / (label + '-after.json')).read_bytes()
        except (OSError, AssertionError) as error:
            errors.append(label + ' frozen input snapshot incomplete/changed: ' + str(error))
    try:
        assert (evidence / 'go-version.txt').read_text().strip() == 'go version go1.26.9 linux/amd64'
        inputs = json.loads((evidence / 'inputs.json').read_text())
        assert len(inputs) == 16 and {(x['phase'], x['path']) for x in inputs} == {
            (phase, PREFIX + name) for phase in ('old', 'new') for name in FORMAT + F_FILES}
        for item in inputs:
            raw = (evidence / 'raw' / item['phase'] / item['path']).read_bytes()
            assert len(raw) == item['bytes'] and sha(raw) == item['sha256']
            if item['purpose'] == 'F-list-only':
                continue
            formatted = (evidence / 'formatted' / item['phase'] / item['path']).read_bytes()
            result = {'phase': item['phase'], 'path': item['path'], 'rawSha256': sha(raw),
                      'formattedSha256': sha(formatted), 'rawChangedByFormatter': raw != formatted, 'operations': {}}
            for operation in ('raw-diff', 'write-copy', 'formatted-diff', 'formatted-list'):
                prefix = evidence / 'results' / item['phase'] / Path(item['path']).name / operation
                status = int(Path(str(prefix) + '.exit').read_text().strip())
                stdout = Path(str(prefix) + '.stdout').read_bytes()
                stderr = Path(str(prefix) + '.stderr').read_bytes()
                result['operations'][operation] = {'actualExit': status, 'stdoutBytes': len(stdout), 'stderrBytes': len(stderr)}
                if operation == 'raw-diff':
                    valid = not stderr and (status == 0 or (status == 1 and bool(stdout))) and bool(stdout) == (raw != formatted)
                else:
                    valid = status == 0 and not stdout and not stderr
                if not valid:
                    errors.append(item['phase'] + '/' + item['path'] + ': invalid actual ' + operation)
            results.append(result)
        for phase in ('old', 'new'):
            prefix = evidence / 'results' / phase / 'reviewed-F5-list'
            assert Path(str(prefix) + '.exit').read_text().strip() == '0'
            assert Path(str(prefix) + '.stdout').read_bytes() == Path(str(prefix) + '.stderr').read_bytes() == b''
        for name in FIXTURES:
            assert (evidence / 'raw/old' / PREFIX / name).read_bytes() == (evidence / 'raw/new' / PREFIX / name).read_bytes()
            assert (evidence / 'formatted/old' / PREFIX / name).read_bytes() == (evidence / 'formatted/new' / PREFIX / name).read_bytes()
    except (OSError, ValueError, KeyError, AssertionError) as error:
        errors.append('formatter or same-fixture evidence incomplete: ' + str(error))
    report = {'oldHead': OLD, 'oldTree': OLD_TREE, 'newHead': NEW, 'newTree': NEW_TREE,
              'controllerHead': os.environ['GITHUB_SHA'], 'fullInputArchives': archives, 'results': results,
              'errors': errors, 'scope': 'format external copies of two identical OLD/NEW fixtures and each production digest; reviewed F5 list only. No tests, compilation, build or application PASS.'}
    save(evidence / 'formatter-actual-receipt.json', report)
    assert not errors and len(results) == 6, errors
    (evidence / 'complete.txt').write_text('Formatter evidence complete; not behavioral or application approval.\n')


if __name__ == '__main__':
    operation = sys.argv[1]
    if operation == 'prepare':
        prepare(*(Path(x).resolve() for x in sys.argv[2:]))
    elif operation == 'snapshot':
        snapshot(Path(sys.argv[2]).resolve(), sys.argv[3], sys.argv[4], Path(sys.argv[5]))
    elif operation == 'judge':
        judge(Path(sys.argv[2]))
    else:
        raise SystemExit('unknown formatter operation')
