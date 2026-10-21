#!/usr/bin/env python3
"""Remote CI only: invoke the real audit-policy CLI on valid and corrupt inputs."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
FIXTURE = ROOT / 'tools/testdata/pnpm-audit-v9-no-vulnerabilities.json'
CASE_OUTCOMES = []


def clean_report():
    return json.loads(FIXTURE.read_text())


def with_advisory(package='follow-redirects', severity='high', ghsa='GHSA-q3jh-wrc6-jgq8'):
    report = clean_report()
    report['advisories']['1'] = {
        'id': 1, 'module_name': package, 'severity': severity,
        'github_advisory_id': ghsa, 'cves': [],
        'findings': [{'version': '1.16.0', 'paths': ['.>axios>follow-redirects']}],
    }
    report['metadata']['vulnerabilities'][severity] = 1
    return report


class AuditPolicyControls(unittest.TestCase):
    def check_cli(self, report, audit_exit, expected, diagnostic=None, exceptions=None):
        with tempfile.TemporaryDirectory(prefix='pnpm-audit-control-', dir=os.environ['RUNNER_TEMP']) as temporary:
            folder = Path(temporary)
            audit = folder / 'audit.json'
            audit.write_text(report if isinstance(report, str) else json.dumps(report))
            status = folder / 'audit.exit'
            status.write_text(audit_exit + '\n')
            exception_path = ROOT / '.github/audit-exceptions.yml'
            if exceptions is not None:
                exception_path = folder / 'exceptions.yml'
                exception_path.write_text(exceptions)
            before = audit.read_bytes()
            completed = subprocess.run(
                [sys.executable, '-B', str(ROOT / 'tools/check_pnpm_audit_exceptions.py'),
                 '--audit', str(audit), '--audit-exit', str(status), '--exceptions', str(exception_path)],
                capture_output=True, text=True, timeout=15)
            CASE_OUTCOMES.append({'test': self.id(), 'subtest': str(self._subtest) if self._subtest else None,
                                  'inputSHA256': hashlib.sha256(before).hexdigest(),
                                  'auditExit': audit_exit, 'expectedCheckerExit': expected,
                                  'actualCheckerExit': completed.returncode,
                                  'stdout': completed.stdout, 'stderr': completed.stderr})
            self.assertEqual(completed.returncode, expected, completed.stdout + completed.stderr)
            self.assertEqual(audit.read_bytes(), before, 'CLI rewrote raw audit evidence')
            if diagnostic:
                self.assertIn(diagnostic, completed.stderr)
            if expected == 0:
                self.assertIn('Audit exceptions validated.', completed.stdout)
            else:
                self.assertNotIn('Audit exceptions validated.', completed.stdout)

    def test_original_official_empty_report(self):
        self.assertEqual(hashlib.sha256(FIXTURE.read_bytes()).hexdigest(),
                         '3d1a667d3f89d6760821df1b70f470b1acdba710ad18d7d315456c345173931d')
        self.check_cli(FIXTURE.read_text(), '0', 0)

    def test_valid_low_only_json_exit_one_preserves_policy(self):
        self.check_cli(with_advisory(severity='low'), '1', 0)

    def test_real_xlsx_exception_remains_valid(self):
        self.check_cli(with_advisory('xlsx', ghsa='GHSA-4r6h-8v6p-xvw6'), '1', 0)

    def test_unexcepted_high_fails(self):
        self.check_cli(with_advisory(), '1', 1, 'missing exceptions')

    def test_expired_exception_fails(self):
        exceptions = '''version: 1
exceptions:
  - package: follow-redirects
    advisory: GHSA-q3jh-wrc6-jgq8
    severity: high
    mitigation: control fixture
    expires_on: 2000-01-01
'''
        self.check_cli(with_advisory(), '1', 1, 'Exceptions expired:', exceptions)

    def test_occurrence_counts_can_exceed_distinct_advisories(self):
        report = with_advisory(severity='low')
        report['metadata']['vulnerabilities']['low'] = 2
        self.check_cli(report, '1', 0)

    def test_invalid_reports_fail_closed(self):
        bad = {
            'empty-object': {}, 'error-only': {'error': {'code': 'ERR_PNPM_AUDIT_BAD_RESPONSE'}},
            'unknown-format': {'vulnerabilities': {}}, 'array-top': [],
            'null-top': None, 'scalar-top': 5, 'empty-stream': '', 'malformed-json': '{',
        }
        for field in ('error', 'errors'):
            report = clean_report()
            report[field] = {}
            bad['complete-with-' + field] = report
        for name, value in bad.items():
            with self.subTest(name=name):
                self.check_cli(value, '1', 1, 'Invalid pnpm 9 audit report:')

    def test_invalid_counts_fail_closed(self):
        for value in (True, -1, 1.5, '0', None):
            with self.subTest(value=value):
                report = clean_report()
                report['metadata']['vulnerabilities']['high'] = value
                self.check_cli(report, '0', 1, 'Invalid pnpm 9 audit report:')

    def test_incomplete_metadata_and_containers_fail_closed(self):
        for field in ('actions', 'muted', 'advisories', 'metadata'):
            with self.subTest(field=field):
                report = clean_report()
                del report[field]
                self.check_cli(report, '0', 1, 'Invalid pnpm 9 audit report:')
        report = clean_report()
        del report['metadata']['dependencies']
        self.check_cli(report, '0', 1, 'Invalid pnpm 9 audit report:')

    def test_malformed_advisories_fail_closed(self):
        mutations = {
            'scalar-entry': lambda report: report['advisories'].update({'1': []}),
            'missing-package': lambda report: report['advisories']['1'].pop('module_name'),
            'missing-severity': lambda report: report['advisories']['1'].pop('severity'),
            'unknown-severity': lambda report: report['advisories']['1'].update(severity='urgent'),
            'bad-id': lambda report: report['advisories']['1'].update(id=True),
            'bad-cves': lambda report: report['advisories']['1'].update(cves='CVE-2026-1'),
            'bad-ghsa': lambda report: report['advisories']['1'].update(github_advisory_id={}),
            'bad-findings': lambda report: report['advisories']['1'].update(findings={}),
            'bad-version': lambda report: report['advisories']['1']['findings'][0].update(version=None),
            'bad-paths': lambda report: report['advisories']['1']['findings'][0].update(paths='hidden'),
        }
        for name, mutate in mutations.items():
            with self.subTest(name=name):
                report = with_advisory()
                mutate(report)
                self.check_cli(report, '1', 1, 'Invalid pnpm 9 audit report:')
        report = with_advisory()
        for field in ('id', 'github_advisory_id', 'cves'):
            del report['advisories']['1'][field]
        self.check_cli(report, '1', 1, 'lacks stable identifier')

    def test_advisory_severity_presence_matches_metadata(self):
        report = clean_report()
        report['metadata']['vulnerabilities']['high'] = 1
        self.check_cli(report, '1', 1, 'severities differ')
        report = with_advisory()
        report['metadata']['vulnerabilities']['high'] = 0
        self.check_cli(report, '0', 1, 'severities differ')

    def test_nonzero_signal_and_invalid_audit_status_fail(self):
        for status in ('2', '137', '-9', '', 'true', '1.0'):
            with self.subTest(status=status):
                self.check_cli(clean_report(), status, 1, 'unexpected pnpm audit exit status')

    def test_status_disagrees_with_report_fails(self):
        self.check_cli(clean_report(), '1', 1, 'exit status disagrees')
        self.check_cli(with_advisory(severity='low'), '0', 1, 'exit status disagrees')

    def test_duplicate_json_key_fails(self):
        self.check_cli('{"error":{},"error":null}', '1', 1, 'duplicate audit JSON key')


if __name__ == '__main__':
    if os.environ.get('GITHUB_ACTIONS') != 'true':
        raise RuntimeError('Run these controls only in remote GitHub CI, never locally or on87')
    expected = unittest.defaultTestLoader.getTestCaseNames(AuditPolicyControls)
    suite = unittest.defaultTestLoader.loadTestsFromTestCase(AuditPolicyControls)
    result = unittest.TextTestRunner(verbosity=2).run(suite)
    report = {'expectedTestNames': expected, 'testsRun': result.testsRun,
              'failures': len(result.failures), 'errors': len(result.errors),
              'skipped': len(result.skipped), 'passed': result.wasSuccessful(),
              'originalOfficialFixtureSHA256': hashlib.sha256(FIXTURE.read_bytes()).hexdigest(),
              'actualCLIInvocations': len(CASE_OUTCOMES), 'caseOutcomes': CASE_OUTCOMES}
    Path(os.environ['AUDIT_CONTROL_REPORT']).write_text(json.dumps(report, indent=2) + '\n')
    sys.exit(0 if result.wasSuccessful() and result.testsRun == len(expected) and not result.skipped else 1)
