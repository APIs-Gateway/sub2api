#!/usr/bin/env python3
"""Run ONLY on remote CI; real Go profile/instrumenter and corruption controls."""
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
import sys

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('complete_git_coverage', HERE / 'check.py')
coverage = importlib.util.module_from_spec(spec)
spec.loader.exec_module(coverage)


class CompleteGitCoverageControls(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if os.environ.get('GITHUB_ACTIONS') != 'true':
            raise RuntimeError('validation is remote GitHub CI only')
        cls.temporary = tempfile.TemporaryDirectory(prefix='complete-git-controls-', dir=os.environ['RUNNER_TEMP'])
        cls.root = Path(cls.temporary.name)
        cls.repo = cls.root / 'repository'
        cls.repo.mkdir()

        def run(*args, cwd=None):
            return subprocess.check_output(args, cwd=cwd or cls.repo)
        cls.execute = staticmethod(run)
        run('git', 'init', '-q')
        run('git', 'config', 'user.email', 'coverage-control@example.invalid')
        run('git', 'config', 'user.name', 'Coverage negative controls')
        run('git', 'commit', '--allow-empty', '-qm', 'Control base')
        cls.base = run('git', 'rev-parse', 'HEAD').decode().strip()
        (cls.repo / 'backend/third_party/req').mkdir(parents=True)
        (cls.repo / '.github/coverage-integrity').mkdir(parents=True)
        shutil.copyfile(HERE / 'inventory.go', cls.repo / '.github/coverage-integrity/inventory.go')
        shutil.copyfile(HERE.parents[1] / 'codecov.yml', cls.repo / 'codecov.yml')
        (cls.repo / 'backend/go.mod').write_text('module github.com/Wei-Shaw/sub2api\n\ngo 1.26.9\n\nrequire github.com/imroc/req/v3 v3.0.0\nreplace github.com/imroc/req/v3 => ./third_party/req\n')
        (cls.repo / 'backend/third_party/req/go.mod').write_text('module github.com/imroc/req/v3\n\ngo 1.26.9\n')
        (cls.repo / 'backend/third_party/req/value.go').write_text(
            'package req\n'
            'func Value() int { return Choose(true) + Choose(false) + 4 }\n'
            'func Choose(flag bool) int {\n'
            'if flag {\n'
            'return 1\n'
            '}\n'
            'return 2\n'
            '}\n')
        (cls.repo / 'backend/value.go').write_text('package app\nimport req "github.com/imroc/req/v3"\nfunc Value() int { return req.Value() }\n')
        (cls.repo / 'backend/value_test.go').write_text('package app\nimport "testing"\nfunc TestValue(t *testing.T) { if Value() != 7 { t.Fatal("value") } }\n')
        # More than 100 changed source files must all remain visible, even when
        # declaration-only files have no executable/instrumentable blocks.
        for number in range(105):
            (cls.repo / 'backend' / ('declaration_%03d.go' % number)).write_text('package app\nconst Declaration%d = %d\n' % (number, number))
        run('git', 'add', '.')
        run('git', 'commit', '-qm', 'Complete fixture with more than 100 paths')
        cls.head = run('git', 'rev-parse', 'HEAD').decode().strip()
        cls.frozen = cls.root / 'frozen'
        coverage.freeze(cls.repo, cls.frozen, cls.head, cls.base)
        cls.profile_root = cls.root / 'profiles'
        cls.profile_root.mkdir()
        raw = cls.profile_root / 'actual.out'
        run('go', 'test', '-count=1', '-covermode=atomic', '-coverpkg=./...,github.com/imroc/req/v3/...',
            '-coverprofile=' + str(raw), './...', cwd=cls.repo / 'backend')
        for name in coverage.PROFILES:
            shutil.copyfile(raw, cls.profile_root / name)

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    def setUp(self):
        self.case = Path(tempfile.mkdtemp(prefix='case-', dir=self.root))
        self.evidence = self.case / 'evidence'
        shutil.copytree(self.frozen, self.evidence)
        self.profiles = self.case / 'profiles'
        shutil.copytree(self.profile_root, self.profiles)

    def check(self, head=None, base=None):
        coverage.check(self.repo, self.evidence, head or self.head, base or self.base, self.profiles)

    def test_complete_real_go_profile_and_full_git_over_100_files(self):
        self.check()
        report = json.loads((self.evidence / 'report.json').read_text())
        self.assertTrue(report['passed'])
        self.assertGreater(report['fullGitChangedPaths'], 100)
        self.assertEqual(report['backendGoChangedFiles'], 107)
        self.assertEqual(report['coveredLines'], report['instrumentedChangedLines'])
        self.assertEqual(len([f for f in report['files'] if f['expectedBlocks'] == 0]), 105)
        baseline = json.loads((self.evidence / 'instrumenter-vs-actual-baseline.json').read_text())
        self.assertTrue(baseline)
        self.assertFalse(any(f['missing'] or f['unexpected'] or f['statementConflicts'] for f in baseline))

    def test_missing_mandatory_profile(self):
        (self.profiles / coverage.PROFILES[0]).unlink()
        with self.assertRaisesRegex(coverage.IntegrityError, 'mandatory profile missing'):
            self.check()

    def rewrite_profiles(self, rewrite):
        for name in coverage.PROFILES:
            path = self.profiles / name
            path.write_text(rewrite(path.read_text()))

    def test_missing_profile_file_records(self):
        self.rewrite_profiles(lambda text: '\n'.join(line for line in text.splitlines() if '/sub2api/value.go:' not in line) + '\n')
        with self.assertRaisesRegex(coverage.IntegrityError, 'coverage profile/source integrity failed'):
            self.check()

    def test_deleted_block_in_compiled_file(self):
        # Keep this compiled file represented, removing just one expected block
        # from every profile in the union. This must fail the independent
        # all-represented-file baseline, not just a whole-file presence check.
        original = (self.profiles / coverage.PROFILES[0]).read_text()
        candidates = [line for line in original.splitlines() if '/req/v3/value.go:' in line]
        self.assertGreater(len(candidates), 1)
        removed = candidates[0]

        def remove_exactly_one(text):
            lines = text.splitlines()
            self.assertEqual(lines.count(removed), 1)
            changed = [line for line in lines if line != removed]
            self.assertEqual(len(changed), len(lines) - 1)
            self.assertTrue(any('/req/v3/value.go:' in line for line in changed))
            return '\n'.join(changed) + '\n'
        self.rewrite_profiles(remove_exactly_one)
        with self.assertRaisesRegex(coverage.IntegrityError, 'legacy instrumenter and actual package coverage coordinates differ'):
            self.check()

    def test_corrupted_coordinates_fail_real_instrumenter_baseline(self):
        self.rewrite_profiles(lambda text: text.replace(':3.18,3.40', ':3.18,3.39') if ':3.18,3.40' in text else text.replace('3.', '999.', 1))
        with self.assertRaises(coverage.IntegrityError):
            self.check()

    def test_source_bytes_changed(self):
        source = self.repo / 'backend/value.go'
        original = source.read_bytes()
        try:
            source.write_bytes(original + b'// changed after the actual coverage run\n')
            with self.assertRaisesRegex(coverage.IntegrityError, 'tracked checkout differs'):
                self.check()
        finally:
            source.write_bytes(original)

    def test_wrong_head(self):
        with self.assertRaisesRegex(coverage.IntegrityError, 'checkout head mismatch'):
            self.check(head=self.base)

    def test_wrong_base(self):
        with self.assertRaisesRegex(coverage.IntegrityError, 'frozen head/base/tree identity differs'):
            self.check(base=self.head)

    def test_unknown_profile_source(self):
        self.rewrite_profiles(lambda text: text + 'backend/phantom.go:1.1,1.5 1 1\n')
        with self.assertRaisesRegex(coverage.IntegrityError, 'unknown/non-Go profile source'):
            self.check()

    def test_partial_line_and_zero_statement_block(self):
        first = (2, 1, 2, 8)
        second = (2, 8, 2, 15)
        result = coverage.assess_file('backend/partial.go', {first: 1, second: 0},
            [{'line': 2, 'start': 1, 'end': 15}], {2},
            {first: {'statements': 1, 'hits': 1, 'profiles': ['unit']},
             second: {'statements': 0, 'hits': 0, 'profiles': ['unit']}}, ['unit'])
        self.assertEqual(result['lines'][0]['state'], 'partial')
        self.assertEqual(result['changedBlockDiagnostic'], {'total': 2, 'hit': 1})
        self.assertFalse(result['integrityProblems'])

    def test_uncompiled_platform_source_counts_miss_not_corruption(self):
        # Real platform classification and independent tool inventory: an
        # executable Windows file exists but this Linux run cannot profile it.
        source = self.repo / 'backend/transport_windows.go'
        source.write_text('package app\n// uncompiled platform control\nfunc Platform() int { return 1 }\n')
        try:
            selected = coverage.selected_sources(self.repo, self.case)
            self.assertNotIn('backend/transport_windows.go', selected)
            helper = self.case / 'inventory'
            self.execute('go', 'build', '-o', str(helper), str(self.repo / '.github/coverage-integrity/inventory.go'))
            expected, tokens = coverage.instrument(self.repo, 'backend/transport_windows.go', self.case, helper)
            self.assertTrue(expected)
            result = coverage.assess_file('backend/transport_windows.go', expected, tokens, {3}, {}, [])
            self.assertEqual(result['lines'][0]['state'], 'miss')
            self.assertTrue(all(block['state'] == 'unprofiled' for block in result['blocks']))
            self.assertFalse(result['integrityProblems'])
            selected_result = coverage.assess_file('backend/transport.go', expected, tokens, {3}, {}, ['unit'])
            self.assertTrue(selected_result['integrityProblems'])
        finally:
            source.unlink()

    def test_comment_only_and_noninstrumented_line_stay_explicit(self):
        result = coverage.assess_file('backend/constant.go', {}, [{'line': 3, 'start': 1, 'end': 12}], {2, 3}, {}, ['unit'])
        self.assertEqual([line['state'] for line in result['lines']], ['not-instrumented', 'not-instrumented'])
        self.assertEqual(result['lines'][0]['reason'], 'comment or whitespace only')
        self.assertIsNotNone(result['noBlocksReason'])

    def test_malformed_atomic_profile(self):
        self.rewrite_profiles(lambda text: text + 'malformed block\n')
        with self.assertRaisesRegex(coverage.IntegrityError, 'malformed profile record'):
            self.check()

    def test_real_token_spans_keep_multiline_raw_string_and_drop_comments(self):
        source = self.repo / 'backend/token_control.go'
        source.write_bytes(b'package app\nfunc Tokens() string {\n// comment only\nreturn `first\r\nsecond`\n}\n')
        try:
            helper = self.case / 'inventory'
            self.execute('go', 'build', '-o', str(helper), str(self.repo / '.github/coverage-integrity/inventory.go'))
            expected, tokens = coverage.instrument(self.repo, 'backend/token_control.go', self.case, helper)
            result = coverage.assess_file('backend/token_control.go', expected, tokens, {3, 4, 5}, {}, [])
            self.assertEqual([line['state'] for line in result['lines']], ['not-instrumented', 'miss', 'miss'])
            self.assertTrue(any(token['line'] == 5 and token['end'] == 8 for token in tokens))
        finally:
            source.unlink()


class RecordedResult(unittest.TextTestResult):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.actual = []

    def addSuccess(self, test):
        self.actual.append({'name': test.id(), 'state': 'passed'})
        super().addSuccess(test)

    def addFailure(self, test, error):
        self.actual.append({'name': test.id(), 'state': 'failed'})
        super().addFailure(test, error)

    def addError(self, test, error):
        self.actual.append({'name': test.id(), 'state': 'error'})
        super().addError(test, error)

    def addSkip(self, test, reason):
        self.actual.append({'name': test.id(), 'state': 'skipped', 'reason': reason})
        super().addSkip(test, reason)


if __name__ == '__main__':
    loader = unittest.TestLoader()
    expected = ['__main__.CompleteGitCoverageControls.' + name for name in loader.getTestCaseNames(CompleteGitCoverageControls)]
    suite = loader.loadTestsFromTestCase(CompleteGitCoverageControls)
    result = unittest.TextTestRunner(verbosity=2, resultclass=RecordedResult).run(suite)
    passed = sorted(item['name'] for item in result.actual if item['state'] == 'passed')
    complete = result.wasSuccessful() and passed == sorted(expected) and result.testsRun == len(expected)
    destination = os.environ.get('COVERAGE_INTEGRITY_EVIDENCE')
    if destination:
        evidence = Path(destination)
        evidence.mkdir(parents=True, exist_ok=True)
        coverage.write_json(evidence / 'negative-controls.json',
                            {'expected': expected, 'actual': result.actual, 'testsRun': result.testsRun, 'passed': complete})
    sys.exit(0 if complete else 1)
