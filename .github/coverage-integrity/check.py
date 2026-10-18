#!/usr/bin/env python3
"""Remote-CI-only complete Git Go coverage lower bound; supplementary to Codecov.

No profile is rewritten. The denominator comes from full Git changes and the
pinned Go instrumenter, not from GitHub compare or whichever files were uploaded.
"""
import argparse
import collections
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import tempfile

PROFILES = ('coverage-unit.out', 'coverage-integration.out',
            'coverage-setup-cli.out', 'coverage-req-http2.out')
EXPECTED_IGNORES = [
    'backend/ent/**', 'backend/cmd/server/wire_gen.go', 'backend/migrations/**',
    'backend/bin/**', 'backend/**/*_test.go', 'frontend/dist/**',
    'frontend/src/main.ts', '**/*.spec.ts', '**/*.test.ts', '**/*.d.ts',
]
MODULE_PATHS = {'github.com/Wei-Shaw/sub2api/': 'backend/',
                'github.com/imroc/req/v3/': 'backend/third_party/req/'}
SHA = re.compile(r'[0-9a-f]{40}')
RECORD = re.compile(r'(.+):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)')


class IntegrityError(Exception):
    pass


def require(condition, message):
    if not condition:
        raise IntegrityError(message)


def command(repo, *args):
    return subprocess.check_output(args, cwd=repo)


def git(repo, *args):
    return command(repo, 'git', *args)


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + '\n')


def identities(repo, head, base):
    require(SHA.fullmatch(head) and SHA.fullmatch(base), 'expected full head/base SHA required')
    require(git(repo, 'rev-parse', 'HEAD').decode().strip() == head, 'checkout head mismatch')
    require(git(repo, 'rev-parse', base + '^{commit}').decode().strip() == base, 'base commit mismatch')
    bases = git(repo, 'merge-base', '--all', base, head).decode().splitlines()
    require(len(bases) == 1, 'a unique full Git merge base is required')
    require(not git(repo, 'diff', '--no-ext-diff', '--no-textconv', 'HEAD', '--'), 'tracked checkout differs')
    return {'head': head, 'base': base, 'mergeBase': bases[0],
            'tree': git(repo, 'rev-parse', head + '^{tree}').decode().strip(),
            'baseTree': git(repo, 'rev-parse', base + '^{tree}').decode().strip(),
            'mergeBaseTree': git(repo, 'rev-parse', bases[0] + '^{tree}').decode().strip()}


def tracked_manifest(repo):
    result = {}
    for entry in git(repo, 'ls-files', '--stage', '-z').split(b'\0'):
        if not entry:
            continue
        metadata, rawpath = entry.split(b'\t', 1)
        mode, blob, stage = metadata.decode().split()
        require(stage == '0', 'unmerged index')
        path = rawpath.decode('utf-8')
        source = repo / path
        require(mode in ('100644', '100755', '120000'), 'submodule/unknown tracked input: ' + path)
        data = os.readlink(source).encode() if mode == '120000' else source.read_bytes()
        result[path] = {'blob': blob, 'mode': mode, 'sha256': sha256(data)}
    require(result, 'empty tracked source manifest')
    return result


def json_stream(data):
    decoder = json.JSONDecoder()
    data = data.decode()
    values = []
    while data.strip():
        value, end = decoder.raw_decode(data.lstrip())
        values.append(value)
        data = data.lstrip()[end:]
    return values


def selected_sources(repo, evidence):
    selected = collections.defaultdict(list)
    for name, args in (
        ('unit', ('-tags=unit', './...')),
        ('integration', ('-tags=integration', './...')),
        ('req', ('github.com/imroc/req/v3/...',)),
    ):
        data = command(repo / 'backend', 'go', 'list', '-json', *args)
        (evidence / ('go-list-' + name + '.json')).write_bytes(data)
        packages = json_stream(data)
        require(packages, 'empty Go source classification: ' + name)
        for package in packages:
            require(not package.get('Error') and not package.get('Incomplete'), 'incomplete go list package')
            directory = Path(package['Dir']).resolve()
            for filename in package.get('GoFiles', []) + package.get('CgoFiles', []):
                path = (directory / filename).relative_to(repo).as_posix()
                require((repo / path).is_file(), 'selected source missing: ' + path)
                selected[path].append(name)
    return dict(selected)


def freeze(repo, evidence, head, base):
    require(not evidence.exists(), 'evidence directory must be fresh')
    evidence.mkdir(parents=True)
    require(not git(repo, 'status', '--porcelain'), 'initial checkout is not clean')
    for profile in PROFILES:
        require(not (repo / 'backend' / profile).exists(), 'stale coverage profile: ' + profile)
    identity = identities(repo, head, base)
    version = command(repo, 'go', 'version').decode().strip()
    require(re.fullmatch(r'go version go1\.26\.9 \S+', version), 'unexpected Go instrumenter version')
    (evidence / 'go-version.txt').write_text(version + '\n')
    environment = command(repo, 'go', 'env', '-json', 'GOOS', 'GOARCH', 'CGO_ENABLED',
                          'GOTOOLCHAIN', 'GOWORK', 'GOFLAGS')
    (evidence / 'go-env.json').write_bytes(environment)
    (evidence / 'git-index.txt').write_bytes(git(repo, 'ls-files', '--stage'))
    selected = selected_sources(repo, evidence)
    require(identities(repo, head, base) == identity, 'source changed during build classification')
    manifest = tracked_manifest(repo)
    require(set(selected) <= manifest.keys(), 'go list selected untracked source')
    state = {'identity': identity, 'manifest': manifest, 'selected': selected,
             'run': {key: os.environ.get(key) for key in
                     ('GITHUB_RUN_ID', 'GITHUB_RUN_ATTEMPT', 'GITHUB_EVENT_NAME', 'GITHUB_WORKFLOW_REF')},
             'goVersion': version, 'goEnvironment': json.loads(environment)}
    write_json(evidence / 'frozen.json', state)


def verify_frozen(repo, frozen, head, base):
    require(identities(repo, head, base) == frozen['identity'], 'frozen head/base/tree identity differs')
    require(tracked_manifest(repo) == frozen['manifest'], 'frozen source bytes/index changed')
    require(command(repo, 'go', 'version').decode().strip() == frozen['goVersion'], 'Go version changed')
    environment = json.loads(command(repo, 'go', 'env', '-json', 'GOOS', 'GOARCH', 'CGO_ENABLED',
                                     'GOTOOLCHAIN', 'GOWORK', 'GOFLAGS'))
    require(environment == frozen['goEnvironment'], 'Go build environment changed')
    run = {key: os.environ.get(key) for key in frozen['run']}
    require(run == frozen['run'], 'coverage evidence belongs to another run')


def full_diff(repo, evidence, merge_base, head):
    options = ('--no-ext-diff', '--no-textconv', '--no-renames')
    numstat = git(repo, 'diff', *options, '--numstat', '-z', merge_base, head, '--')
    names = git(repo, 'diff', *options, '--name-only', '-z', merge_base, head, '--')
    (evidence / 'full-diff.numstat').write_bytes(numstat)
    (evidence / 'full-diff.paths').write_bytes(names)
    (evidence / 'full-diff.patch').write_bytes(git(repo, 'diff', *options, '--unified=0', '--no-color', merge_base, head, '--'))
    records = {}
    for entry in numstat.split(b'\0'):
        if entry:
            added, deleted, path = entry.decode().split('\t', 2)
            require(path not in records, 'duplicate diff path')
            records[path] = {'added': added, 'deleted': deleted}
    require(set(records) == {p.decode() for p in names.split(b'\0') if p}, 'full diff file inventories disagree')
    return records


def changed_lines(repo, evidence, merge_base, head, path, added):
    patch = git(repo, 'diff', '--no-ext-diff', '--no-textconv', '--no-renames',
                '--unified=0', '--no-color', merge_base, head, '--', path)
    (evidence / 'patches' / (sha256(path.encode()) + '.patch')).write_bytes(patch)
    result = set()
    old_left = new_left = 0
    line = None
    for raw in patch.decode().splitlines():
        if raw.startswith('@@ '):
            match = re.match(r'@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@', raw)
            require(match is not None, 'invalid Git hunk')
            require(old_left == new_left == 0, 'truncated Git hunk')
            _, old_count, start, new_count = match.groups()
            old_left, new_left = int(old_count or 1), int(new_count or 1)
            line = int(start)
        elif line is not None and raw.startswith('+'):
            result.add(line)
            line += 1
            new_left -= 1
        elif line is not None and raw.startswith('-'):
            old_left -= 1
        elif line is not None and raw.startswith(' '):
            old_left -= 1
            new_left -= 1
            line += 1
        elif raw.startswith('\\ No newline at end of file'):
            pass
        elif line is not None:
            require(old_left == new_left == 0, 'incomplete Git patch')
        require(old_left >= 0 and new_left >= 0, 'invalid Git hunk counts')
    require(old_left == new_left == 0, 'truncated final Git hunk')
    require(len(result) == int(added), 'Git patch additions do not equal full numstat: ' + path)
    return result


def scope_reason(path, manifest):
    if path not in manifest:
        return 'deleted from head'
    if not path.startswith('backend/') or not path.endswith('.go'):
        return 'outside backend Go scope (still covered by original Codecov policy where applicable)'
    if path.endswith('_test.go'):
        return 'existing Codecov test ignore'
    if path.startswith(('backend/ent/', 'backend/migrations/', 'backend/bin/')) or path == 'backend/cmd/server/wire_gen.go':
        return 'existing Codecov generated/output ignore'
    return None


def profile_source(rawpath, repo, manifest):
    path = rawpath
    for prefix, replacement in MODULE_PATHS.items():
        if path.startswith(prefix):
            path = replacement + path[len(prefix):]
            break
    if Path(path).is_absolute():
        try:
            path = Path(path).relative_to(repo).as_posix()
        except ValueError:
            raise IntegrityError('profile source outside frozen repository: ' + rawpath)
    require(PurePosixPath(path).as_posix() == path and '..' not in PurePosixPath(path).parts,
            'ambiguous coverage source path: ' + rawpath)
    require(path in manifest and path.startswith('backend/') and path.endswith('.go'),
            'unknown/non-Go profile source: ' + rawpath)
    return path


def read_profiles(repo, evidence, manifest, profile_root):
    profiles = {}
    records = collections.defaultdict(dict)
    for name in PROFILES:
        file = profile_root / name
        require(file.is_file() and file.stat().st_size > 0, 'mandatory profile missing: ' + name)
        raw = file.read_bytes()
        (evidence / 'profiles' / name).write_bytes(raw)
        profiles[name] = {'sha256': sha256(raw), 'bytes': len(raw), 'records': 0}
        lines = raw.decode().splitlines()
        require(lines[0] == 'mode: atomic' and len(lines) > 1, 'invalid/empty atomic profile: ' + name)
        for line in lines[1:]:
            match = RECORD.fullmatch(line)
            require(match is not None, 'malformed profile record in ' + name)
            rawpath, *numbers = match.groups()
            path = profile_source(rawpath, repo, manifest)
            sl, sc, el, ec, statements, hits = map(int, numbers)
            require(sl > 0 and sc > 0 and ((sl, sc) < (el, ec) or
                    ((sl, sc) == (el, ec) and statements == 0)), 'invalid coverage block bounds')
            key = sl, sc, el, ec
            previous = records[path].get(key)
            require(previous is None or previous['statements'] == statements, 'conflicting statement counts: ' + path)
            record = records[path].setdefault(key, {'statements': statements, 'hits': 0, 'profiles': []})
            record['hits'] = max(record['hits'], hits)
            if name not in record['profiles']:
                record['profiles'].append(name)
            profiles[name]['records'] += 1
    return records, profiles


def instrument(repo, path, scratch, helper):
    source = repo / path
    require(source.is_file() and not source.is_symlink(), 'missing/symlink inventory source: ' + path)
    data = source.read_bytes()
    # Adjusted //line locations would not identify physical Git added lines.
    require(not re.search(rb'(?m)^//line |/\*line ', data), 'unsupported source line directive: ' + path)
    output = scratch / 'instrumented.go'
    subprocess.run(['go', 'tool', 'cover', '-mode=atomic', '-var=CoverageIntegrity',
                    '-o', str(output), str(source)], cwd=repo, check=True)
    result = json.loads(command(repo, str(helper), str(source), str(output)))
    lines = data.splitlines()
    expected = {}
    for block in result['blocks']:
        key = block['startLine'], block['startCol'], block['endLine'], block['endCol']
        sl, sc, el, ec = key
        require(sl > 0 and sc > 0 and ((sl, sc) < (el, ec) or
                ((sl, sc) == (el, ec) and block['statements'] == 0)) and
                el <= len(lines), 'instrumenter bounds outside source')
        require(sc <= len(lines[sl - 1]) + 1 and ec <= len(lines[el - 1]) + 1, 'instrumenter columns outside source')
        require(key not in expected and block['statements'] >= 0, 'duplicate/invalid instrumenter block')
        expected[key] = block['statements']
    return expected, result['tokens']


def overlaps(key, token):
    sl, sc, el, ec = key
    if (sl, sc) == (el, ec):
        return False
    return (sl, sc) < (token['line'], token['end']) and (token['line'], token['start']) < (el, ec)


def assess_file(path, expected, tokens, changed, actual, selected):
    integrity = []
    for key, record in actual.items():
        if key not in expected or record['statements'] != expected[key]:
            integrity.append('actual profile block differs from independent Go instrumenter: ' + str(key))
    missing = sorted(set(expected) - actual.keys())
    if missing and selected:
        integrity.append('selected/tested source has missing expected profile blocks: ' + str(len(missing)))
    line_blocks = collections.defaultdict(set)
    token_lines = set()
    for token in tokens:
        line = token['line']
        if line not in changed:
            continue
        token_lines.add(line)
        for key in expected:
            if overlaps(key, token):
                line_blocks[line].add(key)
    lines = []
    for line in sorted(changed):
        keys = line_blocks[line]
        hit = [key for key in keys if key in actual and actual[key]['hits'] > 0]
        state = ('hit' if len(hit) == len(keys) else 'partial' if hit else 'miss') if keys else 'not-instrumented'
        lines.append({'line': line, 'state': state, 'blocks': [list(key) for key in sorted(keys)],
                      'reason': None if keys else 'no Go cover block overlaps changed non-comment tokens' if line in token_lines else 'comment or whitespace only'})
    blocks = [{'position': list(key), 'statements': statements,
               'zeroWidth': key[:2] == key[2:],
               'state': 'unprofiled' if key not in actual else 'hit' if actual[key]['hits'] else 'miss',
               'profiles': actual[key]['profiles'] if key in actual else [],
               'hits': actual[key]['hits'] if key in actual else 0}
              for key, statements in sorted(expected.items())]
    changed_keys = set().union(*line_blocks.values()) if line_blocks else set()
    return {'path': path, 'classification': 'selected/tested source' if selected else 'not selected by current build platform/package inventory',
            'selectedBy': selected, 'expectedBlocks': len(expected), 'profileBlocks': len(actual),
            'missingBlocks': [list(key) for key in missing], 'integrityProblems': integrity,
            'noBlocksReason': None if expected else 'pinned Go instrumenter emitted zero blocks (declarations/global initializers are not instrumented)',
            'lines': lines, 'blocks': blocks,
            'changedBlockDiagnostic': {'total': len(changed_keys),
                                       'hit': sum(key in actual and actual[key]['hits'] > 0 for key in changed_keys)},
            'changedStatementDiagnostic': {'total': sum(expected[key] for key in changed_keys),
                                           'hit': sum(expected[key] for key in changed_keys if key in actual and actual[key]['hits'] > 0)}}


def check(repo, evidence, head, base, profile_root=None):
    frozen = json.loads((evidence / 'frozen.json').read_text())
    verify_frozen(repo, frozen, head, base)
    require(not (evidence / 'report.json').exists(), 'coverage evidence already evaluated')
    config = (repo / 'codecov.yml').read_text().split('ignore:\n', 1)
    require(len(config) == 2, 'existing Codecov ignore config missing')
    ignores = [json.loads(line.strip()[2:]) for line in config[1].splitlines() if line.strip().startswith('- ')]
    require(ignores == EXPECTED_IGNORES, 'Codecov ignore list changed; independent review required')
    (evidence / 'profiles').mkdir()
    (evidence / 'patches').mkdir()
    records, profiles = read_profiles(repo, evidence, frozen['manifest'], profile_root or repo / 'backend')
    changed = full_diff(repo, evidence, frozen['identity']['mergeBase'], head)
    files = []
    excluded = []
    with tempfile.TemporaryDirectory(prefix='coverage-integrity-') as directory:
        scratch = Path(directory)
        helper = scratch / 'inventory'
        subprocess.run(['go', 'build', '-o', str(helper), str(Path(__file__).resolve().with_name('inventory.go'))], cwd=repo, check=True)
        # Validate the legacy instrumenter against ALL represented profile files,
        # not just changed files. Coordinate mismatches remain blocking.
        baseline = []
        inventory = {}
        for path, actual in sorted(records.items()):
            expected, tokens = instrument(repo, path, scratch, helper)
            inventory[path] = expected, tokens
            extra = sorted(set(actual) - expected.keys())
            missing = sorted(set(expected) - actual.keys())
            conflicts = [key for key in actual.keys() & expected.keys() if actual[key]['statements'] != expected[key]]
            baseline.append({'path': path, 'expectedBlocks': len(expected), 'profileBlocks': len(actual),
                             'zeroWidthBlocks': [list(key) for key in expected if key[:2] == key[2:]],
                             'unexpected': extra, 'missing': missing, 'statementConflicts': conflicts})
        write_json(evidence / 'instrumenter-vs-actual-baseline.json', baseline)
        require(not any(row['unexpected'] or row['missing'] or row['statementConflicts'] for row in baseline),
                'legacy instrumenter and actual package coverage coordinates differ; no passing scope claim')
        for path, counts in sorted(changed.items()):
            reason = scope_reason(path, frozen['manifest'])
            if reason:
                excluded.append({'path': path, 'reason': reason, **counts})
                continue
            require(counts['added'].isdigit() and counts['deleted'].isdigit(), 'binary Go source is unsupported')
            lines = changed_lines(repo, evidence, frozen['identity']['mergeBase'], head, path, counts['added'])
            if path not in inventory:
                inventory[path] = instrument(repo, path, scratch, helper)
            expected, tokens = inventory[path]
            files.append(assess_file(path, expected, tokens, lines, records.get(path, {}), frozen['selected'].get(path, [])))
    line_counts = collections.Counter(line['state'] for file in files for line in file['lines'])
    denominator = sum(line_counts[state] for state in ('hit', 'partial', 'miss'))
    integrity = [{'path': file['path'], 'problems': file['integrityProblems']} for file in files if file['integrityProblems']]
    threshold = denominator == 0 or 100 * line_counts['hit'] >= 85 * denominator
    report = {'identity': frozen['identity'], 'profiles': profiles,
              'profileCompletenessDefinition': 'All four mandatory profiles must exist. Expected source block completeness is checked on their union; individual profiles legitimately cover different test scopes. Removing a block from one profile is harmless when another retains that same expected block.',
              'metric': 'conservative lower bound for complete Git changed backend Go instrumented token lines; NOT Codecov line coverage',
              'lineDefinition': 'An added head line is in the denominator when a non-comment Go token overlaps an independent Go cover block. It is hit only if every such overlapping block has positive coverage in the union of four mandatory profiles. Uncompiled/unprofiled blocks count as zero. A partial line contributes zero to the numerator.',
              'targetPercent': 85, 'coveredLines': line_counts['hit'], 'instrumentedChangedLines': denominator,
              'thresholdApplicability': 'applicable' if denominator else 'not applicable: complete Git scope contains no changed instrumented Go token lines',
              'lineStates': dict(line_counts), 'integrityProblems': integrity,
              'thresholdPassed': threshold, 'passed': not integrity and threshold,
              'fullGitChangedPaths': len(changed), 'backendGoChangedFiles': len(files),
              'allChangedPathDispositions': excluded, 'files': files,
              'scopeLimitation': 'Supplementary backend Go evidence only. Original required Codecov patch>=85 and all actual test results remain required.'}
    write_json(evidence / 'report.json', report)
    verify_frozen(repo, frozen, head, base)
    # Prove the gate left the actual uploaded files byte-for-byte unchanged.
    for name, metadata in profiles.items():
        require(sha256(((profile_root or repo / 'backend') / name).read_bytes()) == metadata['sha256'], 'profile mutated: ' + name)
    print(json.dumps({key: value for key, value in report.items() if key not in ('files', 'allChangedPathDispositions')}, indent=2))
    require(not integrity, 'coverage profile/source integrity failed')
    require(threshold, 'complete Git changed instrumented line coverage lower bound is below 85%')
    (evidence / 'complete.txt').write_text('complete\n')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('phase', choices=('freeze', 'check'))
    parser.add_argument('--repo', type=Path, required=True)
    parser.add_argument('--evidence', type=Path, required=True)
    parser.add_argument('--head', required=True)
    parser.add_argument('--base', required=True)
    args = parser.parse_args()
    require(os.environ.get('GITHUB_ACTIONS') == 'true', 'executable validation is remote GitHub CI only')
    require(args.evidence.resolve().is_relative_to(Path(os.environ['RUNNER_TEMP']).resolve()), 'evidence must be in runner temporary storage')
    repo = args.repo.resolve()
    try:
        (freeze if args.phase == 'freeze' else check)(repo, args.evidence, args.head, args.base)
    except Exception as error:
        args.evidence.mkdir(parents=True, exist_ok=True)
        write_json(args.evidence / ('failure-' + args.phase + '.json'), {'type': type(error).__name__, 'error': str(error)})
        raise


if __name__ == '__main__':
    main()
