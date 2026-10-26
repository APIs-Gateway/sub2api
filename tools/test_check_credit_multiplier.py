#!/usr/bin/env python3
"""check_credit_multiplier.py 的单测：python3 tools/test_check_credit_multiplier.py"""
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import check_credit_multiplier as gate  # noqa: E402


class GateTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = self._tmp.name
        os.makedirs(os.path.join(self.root, ".github"))
        self.allowlist = os.path.join(self.root, ".github", "credit-multiplier-allowlist.txt")

    def tearDown(self):
        self._tmp.cleanup()

    def write(self, rel, text):
        path = os.path.join(self.root, rel)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w", encoding="utf-8") as handle:
            handle.write(text)

    def allow(self, *paths):
        with open(self.allowlist, "w", encoding="utf-8") as handle:
            handle.write("# 注释\n" + "\n".join(paths) + "\n")

    def run_gate(self):
        return gate.main(["--root", self.root])

    def test_equivalent_spellings_are_all_detected(self):
        spellings = [
            "cfg.BalanceRechargeMultiplier",
            'key := "BALANCE_RECHARGE_MULTIPLIER"',
            'json:"balance_recharge_multiplier"',
            "vals[SettingBalanceRechargeMult]",
            "normalizeBalanceRechargeMultiplier(x)",
            "defaultBalanceRechargeMultiplier",
            "balanceRechargeMultiplier := 1",
            "BALANCERECHARGEMULT",
        ]
        for text in spellings:
            with self.subTest(text=text):
                self.write("backend/internal/service/x.go", "package service\n" + text + "\n")
                self.allow()
                self.assertEqual(self.run_gate(), 1)

    def test_clean_tree_passes(self):
        self.write("backend/internal/service/clean.go", "package service\nfunc F() {}\n")
        self.allow()
        self.assertEqual(self.run_gate(), 0)

    def test_allowlisted_file_passes(self):
        self.write("backend/internal/service/payment_amounts.go", "package service\nconst defaultBalanceRechargeMultiplier = 1.0\n")
        self.allow("backend/internal/service/payment_amounts.go")
        self.assertEqual(self.run_gate(), 0)

    def test_new_reader_outside_allowlist_fails(self):
        self.write("backend/internal/service/payment_amounts.go", "package service\nvar _ = BalanceRechargeMultiplier\n")
        self.write("backend/internal/service/new_reader.go", "package service\nvar _ = cfg.BalanceRechargeMultiplier\n")
        self.allow("backend/internal/service/payment_amounts.go")
        violations, stale = gate.check(self.root, self.allowlist)
        self.assertEqual(sorted(violations), ["backend/internal/service/new_reader.go"])
        self.assertEqual(stale, [])
        self.assertEqual(self.run_gate(), 1)

    def test_test_files_are_not_checked(self):
        self.write("backend/internal/service/x_test.go", "package service\nvar _ = BalanceRechargeMultiplier\n")
        self.allow()
        self.assertEqual(self.run_gate(), 0)

    def test_non_go_files_and_other_dirs_are_not_checked(self):
        self.write("backend/migrations/001.sql", "-- BALANCE_RECHARGE_MULTIPLIER\n")
        self.write("docs/spec.md", "BALANCE_RECHARGE_MULTIPLIER\n")
        self.write("frontend/src/a.ts", "balance_recharge_multiplier\n")
        self.allow()
        self.assertEqual(self.run_gate(), 0)

    def test_stale_allowlist_entries_are_reported_but_do_not_fail(self):
        self.write("backend/internal/service/clean.go", "package service\n")
        self.allow("backend/internal/service/gone.go")
        violations, stale = gate.check(self.root, self.allowlist)
        self.assertEqual(violations, {})
        self.assertEqual(stale, ["backend/internal/service/gone.go"])
        self.assertEqual(self.run_gate(), 0)

    def test_missing_allowlist_is_an_error(self):
        self.assertEqual(gate.main(["--root", self.root, "--allowlist", os.path.join(self.root, "nope.txt")]), 2)

    def test_hits_report_line_numbers(self):
        self.write("backend/a.go", "package a\n\nvar x = BalanceRechargeMultiplier\n")
        hits = gate.find_hits(self.root)
        self.assertEqual(hits["backend/a.go"][0][0], 3)

    def test_allowlist_ignores_comments_and_blank_lines(self):
        with open(self.allowlist, "w", encoding="utf-8") as handle:
            handle.write("# 头注释\n\nbackend/a.go  # 行尾注释\n   \n")
        self.assertEqual(gate.load_allowlist(self.allowlist), {"backend/a.go"})


if __name__ == "__main__":
    unittest.main()
