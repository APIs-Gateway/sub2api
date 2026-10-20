#!/usr/bin/env python3
"""CI-only regressions using the complete actual cold/warm module graph."""
import copy
import importlib.util
import json
from pathlib import Path
import unittest

HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("req_security_evidence", HERE / "req-http2-security-evidence.py")
CHECKER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CHECKER)


class OldGraphCacheRegression(unittest.TestCase):
    def setUp(self):
        fixture = json.loads((HERE / "testdata/req-old-graph-cold-warm-ci.json").read_text())
        self.current = fixture["current"]
        self.old = fixture["old"]
        self.backend = Path(fixture["backend"])
        self.evidence = Path(fixture["evidence"])

    def compare(self):
        return CHECKER.compare_old_module_graphs(self.current, self.old, self.backend, self.evidence)

    def external(self, values):
        return next(value for value in values if value["Path"] == "golang.org/x/net")

    def req(self, values):
        return next(value for value in values if value["Path"] == CHECKER.MODULE)

    def main(self, values):
        return next(value for value in values if value.get("Main"))

    def test_actual_cold_to_warm_11_optional_dirs_without_input_mutation(self):
        before = copy.deepcopy((self.current, self.old))
        proof = self.compare()
        self.assertEqual(proof["identicalModules"], 365)
        changes = proof["optionalExternalCacheDirChanges"]
        self.assertEqual([item["path"] for item in changes], [
            "github.com/andybalholm/brotli", "github.com/google/go-querystring",
            "github.com/icholy/digest", "github.com/klauspost/compress",
            "github.com/quic-go/qpack", "github.com/quic-go/quic-go",
            "github.com/refraction-networking/utls", "golang.org/x/crypto",
            "golang.org/x/net", "golang.org/x/sys", "golang.org/x/text",
        ])
        for item in changes:
            self.assertIsNone(item["currentDir"])
            self.assertTrue(Path(item["oldDir"]).is_absolute())
        self.assertEqual((self.current, self.old), before)

    def test_actual_warm_to_cold_optional_dirs(self):
        for before, after in zip(self.current, self.old):
            if not before.get("Main") and before["Path"] != CHECKER.MODULE:
                a, b = before.pop("Dir", None), after.pop("Dir", None)
                if b is not None:
                    before["Dir"] = b
                if a is not None:
                    after["Dir"] = a
        changes = self.compare()["optionalExternalCacheDirChanges"]
        self.assertEqual(len(changes), 11)
        self.assertTrue(all(item["currentDir"] and item["oldDir"] is None for item in changes))

    def test_both_warm_cache_has_no_optional_difference(self):
        for before, after in zip(self.current, self.old):
            if not before.get("Main") and before["Path"] != CHECKER.MODULE and "Dir" in after:
                before["Dir"] = after["Dir"]
        self.assertEqual(self.compare()["optionalExternalCacheDirChanges"], [])

    def test_both_cold_cache_has_no_optional_difference(self):
        for values in (self.current, self.old):
            for value in values:
                if not value.get("Main") and value["Path"] != CHECKER.MODULE:
                    value.pop("Dir", None)
        self.assertEqual(self.compare()["optionalExternalCacheDirChanges"], [])

    def test_version_drift_is_rejected(self):
        self.external(self.old)["Version"] = "v99.0.0"
        with self.assertRaises(SystemExit):
            self.compare()

    def test_sum_drift_is_rejected(self):
        self.external(self.old)["Sum"] = "h1:incorrect-module-sum"
        with self.assertRaises(SystemExit):
            self.compare()

    def test_gomod_sum_drift_is_rejected(self):
        self.external(self.old)["GoModSum"] = "h1:incorrect-modfile-sum"
        with self.assertRaises(SystemExit):
            self.compare()

    def test_go_version_drift_is_rejected(self):
        self.external(self.old)["GoVersion"] = "99.0.0"
        with self.assertRaises(SystemExit):
            self.compare()

    def test_release_time_drift_is_rejected(self):
        self.external(self.old)["Time"] = "1900-01-01T00:00:00Z"
        with self.assertRaises(SystemExit):
            self.compare()

    def test_external_gomod_pointer_drift_is_rejected(self):
        self.external(self.old)["GoMod"] = "/tmp/unrelated/go.mod"
        with self.assertRaises(SystemExit):
            self.compare()

    def test_unknown_module_field_drift_is_rejected(self):
        self.external(self.old)["UnexpectedMetadata"] = "must not be discarded"
        with self.assertRaises(SystemExit):
            self.compare()

    def test_same_external_local_replacement_in_both_graphs_is_rejected(self):
        for values in (self.current, self.old):
            self.external(values)["Replace"] = {"Path": "/tmp/untrusted", "Dir": "/tmp/untrusted"}
        with self.assertRaises(SystemExit):
            self.compare()

    def test_missing_external_version_even_in_both_graphs_is_rejected(self):
        for values in (self.current, self.old):
            self.external(values).pop("Version")
        with self.assertRaises(SystemExit):
            self.compare()

    def test_main_modfile_pointer_drift_is_rejected(self):
        self.main(self.old)["GoMod"] = "/tmp/unrelated-main.mod"
        with self.assertRaises(SystemExit):
            self.compare()

    def test_extra_main_flag_is_rejected(self):
        self.external(self.old)["Main"] = True
        with self.assertRaises(SystemExit):
            self.compare()

    def test_req_replacement_path_drift_is_rejected(self):
        self.req(self.old)["Replace"]["Path"] = "/tmp/unrelated"
        with self.assertRaises(SystemExit):
            self.compare()

    def test_req_replacement_dir_drift_is_rejected(self):
        self.req(self.old)["Replace"]["Dir"] = "/tmp/unrelated"
        with self.assertRaises(SystemExit):
            self.compare()

    def test_req_top_level_source_pointer_drift_is_rejected(self):
        self.req(self.old)["Dir"] = "/tmp/unrelated"
        with self.assertRaises(SystemExit):
            self.compare()

    def test_req_replacement_modfile_drift_is_rejected(self):
        self.req(self.old)["Replace"]["GoMod"] = "/tmp/unrelated/go.mod"
        with self.assertRaises(SystemExit):
            self.compare()

    def test_req_replacement_nonlocation_field_drift_is_rejected(self):
        self.req(self.old)["Replace"]["GoVersion"] = "99.0.0"
        with self.assertRaises(SystemExit):
            self.compare()

    def test_truncated_module_graph_is_rejected(self):
        self.old.pop()
        with self.assertRaises(SystemExit):
            self.compare()

    def test_duplicate_module_path_is_rejected(self):
        self.old[-1] = copy.deepcopy(self.old[-2])
        with self.assertRaises(SystemExit):
            self.compare()

    def test_reordered_module_graph_is_rejected(self):
        self.old[-1], self.old[-2] = self.old[-2], self.old[-1]
        with self.assertRaises(SystemExit):
            self.compare()

    def test_different_present_external_dirs_are_rejected(self):
        self.external(self.current)["Dir"] = "/tmp/different-cache-location"
        with self.assertRaises(SystemExit):
            self.compare()

    def test_relative_optional_cache_dir_is_rejected(self):
        self.external(self.old)["Dir"] = "relative/cache"
        with self.assertRaises(SystemExit):
            self.compare()


if __name__ == "__main__":
    unittest.main(verbosity=2)
