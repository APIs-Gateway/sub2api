#!/usr/bin/env python3
"""CI-only regressions using the complete actual cold/warm module graph."""
import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest import mock

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


class NamedInventoryRegression(unittest.TestCase):
    """Synthetic verifier inputs only; never Go execution or coverage evidence."""

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="req-named-verifier-")
        self.addCleanup(self.temporary.cleanup)
        self.evidence = Path(self.temporary.name)
        self.backend = HERE.parent
        self.inventory = json.loads((HERE / "req-http2-security-inventory.json").read_text())
        self.values = []
        packages = set()
        for key, leaves in self.inventory.items():
            if not key.endswith("Leaves") or not isinstance(leaves, list):
                continue
            kind = key.removesuffix("Leaves")
            package = self.inventory[kind + "Package"]
            packages.add(package)
            for name in leaves:
                self.values.append({"Action": "run", "Package": package, "Test": name})
                if kind == "parallelDownloadInProcess":
                    self.values.append({"Action": "output", "Package": package, "Test": name,
                                        "Output": "G_IN_PROCESS_EXECUTED " + name + "\n" +
                                                  "G_IN_PROCESS_BODY_DONE " + name + "\n"})
                self.values.append({"Action": "pass", "Package": package, "Test": name})
        self.values.extend({"Action": "pass", "Package": package} for package in sorted(packages))
        self.proof = self.evidence / "synthetic.json.inventory-proof.json"

    def verify(self, values, filename="synthetic.json", expected=None):
        (self.evidence / filename).write_text("".join(json.dumps(value) + "\n" for value in values))
        if expected is None:
            return CHECKER.verify_named(self.backend, self.evidence, filename)
        with mock.patch.object(CHECKER, "inventory", return_value=expected):
            return CHECKER.verify_named(self.backend, self.evidence, filename)

    def rejected(self, values, diagnostic, filename="synthetic.json", expected=None):
        proof = self.evidence / (filename + ".inventory-proof.json")
        proof.unlink(missing_ok=True)
        with self.assertRaisesRegex(SystemExit, diagnostic):
            self.verify(values, filename, expected)
        self.assertFalse(proof.exists(), "Rejected input must not publish a complete proof")

    def test_complete_registered_events_are_accepted_as_verifier_control_only(self):
        self.verify(self.values)
        proof = json.loads(self.proof.read_text())
        self.assertEqual(len(proof), 880)
        self.assertEqual(len({(item["package"], item["test"]) for item in proof}), 880)

    def test_missing_original_default_output_leaf_is_rejected(self):
        name = self.inventory["parallelDownloadDefaultOutputLeaves"][0]
        self.rejected([value for value in self.values if value.get("Test") != name], "Missing/duplicate RUN")

    def test_each_missing_http2_receive_flow_leaf_is_rejected(self):
        for name in self.inventory["http2ReceiveFlowLeaves"]:
            with self.subTest(name=name):
                self.rejected([value for value in self.values if value.get("Test") != name], "Missing/duplicate RUN")

    def test_each_missing_dump_ownership_leaf_is_rejected(self):
        for name in self.inventory["dumpOwnershipLeaves"]:
            with self.subTest(name=name):
                self.rejected([value for value in self.values if value.get("Test") != name], "Missing/duplicate RUN")

    def test_each_missing_custom_tls_leaf_is_rejected_by_package(self):
        for kind in ("customTLS", "customTLSHTTP2"):
            package = self.inventory[kind + "Package"]
            for name in self.inventory[kind + "Leaves"]:
                with self.subTest(package=package, name=name):
                    self.rejected([value for value in self.values
                                   if (value.get("Package"), value.get("Test")) != (package, name)],
                                  "Missing/duplicate RUN")

    def test_unknown_custom_tls_leaf_in_each_package_is_rejected(self):
        for kind in ("customTLS", "customTLSHTTP2"):
            package = self.inventory[kind + "Package"]
            with self.subTest(package=package):
                unknown = {"Package": package, "Test": "TestReqCustomTLS_Unregistered"}
                self.rejected(self.values + [dict(unknown, Action="run"), dict(unknown, Action="pass")],
                              "Actual security leaf inventory differs")

    def test_unknown_dump_ownership_leaf_is_rejected(self):
        unknown = {"Package": self.inventory["dumpOwnershipPackage"],
                   "Test": "TestReqDumpOwnershipUnregistered"}
        self.rejected(self.values + [dict(unknown, Action="run"), dict(unknown, Action="pass")],
                      "Actual security leaf inventory differs")

    def test_duplicate_registered_run_is_rejected(self):
        name = self.inventory["http2ReceiveFlowLeaves"][0]
        run = next(value for value in self.values if value.get("Test") == name and value["Action"] == "run")
        self.rejected(self.values + [copy.deepcopy(run)], "Missing/duplicate RUN")

    def test_unknown_http2_receive_flow_leaf_is_rejected(self):
        unknown = {"Package": self.inventory["http2ReceiveFlowPackage"],
                   "Test": "TestReqHTTP2ReceiveFlowContract_Unregistered"}
        self.rejected(self.values + [dict(unknown, Action="run"), dict(unknown, Action="pass")],
                      "Actual security leaf inventory differs")

    def test_failed_registered_test_is_rejected(self):
        values = copy.deepcopy(self.values)
        name = self.inventory["http2ReceiveFlowLeaves"][0]
        next(value for value in values if value.get("Test") == name and value["Action"] == "pass")["Action"] = "fail"
        self.rejected(values, "Failed/skipped test event")

    def test_actual_race_failure_event_shape_is_rejected(self):
        values = copy.deepcopy(self.values)
        name = self.inventory["http2ReceiveFlowLeaves"][0]
        package = self.inventory["http2ReceiveFlowPackage"]
        next(value for value in values if value.get("Test") == name and value["Action"] == "pass")["Action"] = "fail"
        next(value for value in values if value.get("Package") == package and "Test" not in value)["Action"] = "fail"
        values.append({"Action": "output", "Package": package, "Test": name,
                       "Output": "WARNING: DATA RACE\n    testing.go: race detected during execution of test\n"})
        # Go emits failure events and exits nonzero on a real race. A string
        # containing "race" alongside an all-PASS stream is not this control.
        self.rejected(values, "Failed/skipped test event")

    EXPANSION = (("publicAPI", CHECKER.MODULE, 70),
                 ("http1Boundary", CHECKER.MODULE, 33),
                 ("publicDefault", CHECKER.MODULE, 6),
                 ("bisectContract", CHECKER.MODULE + "/internal/bisect", 112),
                 ("http2Coverage", CHECKER.MODULE + "/internal/http2", 51),
                 ("safariWrapper", CHECKER.MODULE, 3))
    SYNTHETIC_FILENAMES = ("synthetic-named.json", "synthetic-full.json")

    def test_original605_fields_order_and_lists_remain_and_expansion_is275(self):
        # Canonical pretty JSON deliberately keeps original key and list order.
        # This anchor was read from reviewed39dd605 source, not actual Go output.
        expansion_keys = {kind + suffix for kind, _, _ in self.EXPANSION
                          for suffix in ("Package", "Count", "Leaves")}
        legacy = {key: value for key, value in self.inventory.items()
                  if key not in expansion_keys}
        legacy["totalNamedLeaves"] = 605
        digest = hashlib.sha256((json.dumps(legacy, indent=2) + "\n").encode()).hexdigest()
        self.assertEqual(digest, "c56238bf90a9dd220a5876b3335a77ec8c6366419df6699c70310ec29df5a438")
        legacy_ids = sorted((legacy[key.removesuffix("Leaves") + "Package"], name)
                            for key, leaves in legacy.items()
                            if key.endswith("Leaves") and isinstance(leaves, list)
                            for name in leaves)
        self.assertEqual(len(legacy_ids), 605)
        self.assertEqual(hashlib.sha256(json.dumps(legacy_ids, separators=(",", ":")).encode()).hexdigest(),
                         "d40da89d1c9592dea18a8457de03b430eb715f633f0aa6215bacb1b7c26be8c8")
        self.assertEqual(sum(count for _, _, count in self.EXPANSION), 275)
        expected_key_suffix = [kind + suffix for kind, _, _ in self.EXPANSION
                               for suffix in ("Package", "Count", "Leaves")]
        self.assertEqual(list(self.inventory)[-len(expected_key_suffix):], expected_key_suffix)

    def test_synthetic_named_and_full_complete880_are_verifier_controls_only(self):
        for filename in self.SYNTHETIC_FILENAMES:
            with self.subTest(filename=filename):
                self.verify(self.values, filename)
                proof = json.loads((self.evidence / (filename + ".inventory-proof.json")).read_text())
                self.assertEqual(len(proof), 880)
                self.assertEqual(len({(item["package"], item["test"]) for item in proof}), 880)
                # The indexed implementation must retain exact original event order.
                for item in proof:
                    identity = (item["package"], item["test"])
                    self.assertEqual(item["actions"], [value["Action"] for value in self.values
                                     if (value.get("Package"), value.get("Test")) == identity])

    def test_each275_missing_run_or_pass_rejects_in_both_synthetic_modes(self):
        for kind, package, count in self.EXPANSION:
            self.assertEqual(self.inventory[kind + "Package"], package)
            self.assertEqual(len(self.inventory[kind + "Leaves"]), count)
            for name in self.inventory[kind + "Leaves"]:
                for action in ("run", "pass"):
                    values = [value for value in self.values if not
                              ((value.get("Package"), value.get("Test")) == (package, name)
                               and value["Action"] == action)]
                    for filename in self.SYNTHETIC_FILENAMES:
                        with self.subTest(kind=kind, name=name, action=action, filename=filename):
                            self.rejected(values, "Missing/duplicate " + action.upper(), filename)

    def test_each_expansion_group_duplicate_failed_skipped_or_wrong_package_rejects(self):
        for kind, package, _ in self.EXPANSION:
            name = self.inventory[kind + "Leaves"][0]
            wrong_package = (CHECKER.MODULE + "/internal/http2"
                             if package == CHECKER.MODULE else CHECKER.MODULE)
            for mutation in ("duplicate_run", "duplicate_pass", "fail", "skip", "wrong_package"):
                values = copy.deepcopy(self.values)
                selected = [value for value in values
                            if (value.get("Package"), value.get("Test")) == (package, name)]
                diagnostic = "Missing/duplicate RUN"
                if mutation.startswith("duplicate_"):
                    action = mutation.removeprefix("duplicate_")
                    values.append(copy.deepcopy(next(value for value in selected if value["Action"] == action)))
                    diagnostic = "Missing/duplicate " + action.upper()
                elif mutation in ("fail", "skip"):
                    next(value for value in selected if value["Action"] == "pass")["Action"] = mutation
                    diagnostic = "Failed/skipped test event"
                else:
                    for value in selected:
                        value["Package"] = wrong_package
                for filename in self.SYNTHETIC_FILENAMES:
                    with self.subTest(kind=kind, mutation=mutation, filename=filename):
                        self.rejected(values, diagnostic, filename)

    def test_each_required_package_missing_or_duplicate_terminal_pass_rejects(self):
        for package in sorted({value["Package"] for value in self.values if "Test" not in value}):
            terminal = next(value for value in self.values
                            if value.get("Package") == package and "Test" not in value)
            for duplicate in (False, True):
                values = (self.values + [copy.deepcopy(terminal)] if duplicate else
                          [value for value in self.values if value is not terminal])
                for filename in self.SYNTHETIC_FILENAMES:
                    with self.subTest(package=package, duplicate=duplicate, filename=filename):
                        self.rejected(values, "Missing.*package PASS", filename)

    def test_unknown_new_prefix_rejects_in_declared_and_other_existing_packages(self):
        families = (("TestReqPublicAPI_", CHECKER.MODULE),
                    ("TestReqH1Boundary_", CHECKER.MODULE),
                    ("TestReqPublicDefaultContract", CHECKER.MODULE),
                    ("TestReqBisectContract_", CHECKER.MODULE + "/internal/bisect"),
                    ("TestReqHTTP2CoverageContract_", CHECKER.MODULE + "/internal/http2"),
                    ("TestReqHTTP2CoverageFrame_", CHECKER.MODULE + "/internal/http2"),
                    ("TestReqSafariWrapperContract", CHECKER.MODULE))
        for prefix, declared_package in families:
            other = (CHECKER.MODULE + "/internal/http2"
                     if declared_package == CHECKER.MODULE else CHECKER.MODULE)
            for package in (declared_package, other):
                unknown = {"Package": package, "Test": prefix + "Unregistered"}
                # Keep every mandatory pair intact, so only unknown identity can reject.
                values = self.values + [dict(unknown, Action="run"), dict(unknown, Action="pass")]
                for filename in self.SYNTHETIC_FILENAMES:
                    with self.subTest(prefix=prefix, package=package, filename=filename):
                        self.rejected(values, "Actual security leaf inventory differs", filename)

    def test_same_count_cross_group_duplicate_identity_rejects(self):
        expected = copy.deepcopy(self.inventory)
        expected["publicAPILeaves"][0] = expected["publicDefaultLeaves"][0]
        for filename in self.SYNTHETIC_FILENAMES:
            with self.subTest(filename=filename):
                self.rejected(self.values, "Duplicate registered package/test identity", filename, expected)

    def test_expansion_wrong_count_package_or_total_rejects(self):
        for kind, _, _ in self.EXPANSION:
            for suffix, value in (("Count", -1), ("Package", "unregistered.invalid/module")):
                expected = copy.deepcopy(self.inventory)
                expected[kind + suffix] = value
                for filename in self.SYNTHETIC_FILENAMES:
                    with self.subTest(kind=kind, suffix=suffix, filename=filename):
                        self.rejected(self.values, "Invalid expansion registry", filename, expected)
        expected = copy.deepcopy(self.inventory)
        expected["totalNamedLeaves"] = 879
        for filename in self.SYNTHETIC_FILENAMES:
            with self.subTest(filename=filename):
                self.rejected(self.values, "Require original605 plus275", filename, expected)

    def test_malformed_unhashable_package_or_test_identity_rejects_explicitly(self):
        for field, invalid in (("Package", []), ("Package", {}), ("Package", None),
                               ("Package", ""), ("Test", []), ("Test", {}),
                               ("Test", None), ("Test", "")):
            values = copy.deepcopy(self.values)
            values[0][field] = invalid
            for filename in self.SYNTHETIC_FILENAMES:
                with self.subTest(field=field, invalid=invalid, filename=filename):
                    self.rejected(values, "Malformed package/test event identity", filename)


class AdaptedOriginalCompletionRegression(unittest.TestCase):
    """Verifier-only controls; these do not execute original Go tests."""

    def setUp(self):
        self.backend = HERE.parent
        self.names = ("TestSetFile", "TestTraceInfo", "TestEnableDumpToFIle", "TestEnableDumpAllToFile")
        self.values = [{"Action": action, "Package": CHECKER.MODULE, "Test": name}
                       for name in self.names for action in ("run", "pass")]

    def test_all_four_original_top_level_completions_are_required(self):
        proof = CHECKER.verify_adapted_original_tests(self.backend, self.values)
        self.assertEqual({item["test"] for item in proof}, set(self.names))

    def test_each_missing_original_is_rejected(self):
        for name in self.names:
            with self.subTest(name=name):
                with self.assertRaisesRegex(SystemExit, "Missing/duplicate original RUN"):
                    CHECKER.verify_adapted_original_tests(
                        self.backend, [value for value in self.values if value["Test"] != name])

    def test_duplicate_original_run_is_rejected(self):
        with self.assertRaisesRegex(SystemExit, "Missing/duplicate original RUN"):
            CHECKER.verify_adapted_original_tests(self.backend, self.values + [self.values[0]])

    def test_missing_duplicate_failed_or_skipped_original_pass_is_rejected(self):
        for action in (None, "pass", "fail", "skip"):
            with self.subTest(action=action):
                values = copy.deepcopy(self.values)
                if action is None:
                    del values[1]
                elif action == "pass":
                    values.append(copy.deepcopy(values[1]))
                else:
                    values[1]["Action"] = action
                with self.assertRaisesRegex(SystemExit, "Missing/duplicate or failed original PASS"):
                    CHECKER.verify_adapted_original_tests(self.backend, values)


class ProductionAdaptationPartitionRegression(unittest.TestCase):
    """Partition controls only; no synthetic profile or coverage PASS claims."""

    def setUp(self):
        self.manifest = json.loads((HERE.parent / "third_party/req/UPSTREAM-MANIFEST.json").read_text())

    def test_original_thirteen_and_three_owned_dump_adaptations_are_required(self):
        paths = CHECKER.production_adaptation_paths(self.manifest)
        self.assertEqual(len(paths), 18)
        self.assertEqual(set(paths[13:16]), {"client.go", "dump.go", "transport.go"})
        self.assertEqual(paths[16], "client_wrapper.go")
        self.assertEqual(paths[17], "internal/bisect/bisect.go")
        self.assertEqual(self.manifest["originalUnmodifiedFileCount"], 112)
        self.assertEqual(len(self.manifest["originalFiles"]), 132)
        self.assertEqual(len(self.manifest["originalTestAdaptations"]), 2)

    def test_each_missing_declared_or_candidate_path_is_rejected(self):
        for key in ("allowedProductionAdaptations", "adaptedCandidateFiles"):
            for index in range(18):
                with self.subTest(key=key, index=index):
                    manifest = copy.deepcopy(self.manifest)
                    if key == "allowedProductionAdaptations":
                        del manifest[key][list(manifest[key])[index]]
                    else:
                        del manifest[key][index]
                    with self.assertRaisesRegex(SystemExit, "source/per-file85 partition"):
                        CHECKER.production_adaptation_paths(manifest)

    def test_same_count_unknown_declared_path_is_rejected(self):
        manifest = copy.deepcopy(self.manifest)
        provenance = manifest["allowedProductionAdaptations"].pop("transport.go")
        manifest["allowedProductionAdaptations"]["unregistered.go"] = provenance
        with self.assertRaisesRegex(SystemExit, "source/per-file85 partition"):
            CHECKER.production_adaptation_paths(manifest)

    def test_wrong_shape_declared_partition_is_rejected(self):
        manifest = copy.deepcopy(self.manifest)
        manifest["allowedProductionAdaptations"] = list(manifest["allowedProductionAdaptations"])
        with self.assertRaisesRegex(SystemExit, "source/per-file85 partition"):
            CHECKER.production_adaptation_paths(manifest)

    def test_same_count_unknown_or_duplicate_candidate_path_is_rejected(self):
        for replacement in ("unregistered.go", "internal/http2/flow.go"):
            with self.subTest(replacement=replacement):
                manifest = copy.deepcopy(self.manifest)
                manifest["adaptedCandidateFiles"][-1]["path"] = replacement
                with self.assertRaisesRegex(SystemExit, "source/per-file85 partition"):
                    CHECKER.production_adaptation_paths(manifest)

    def assert_adaptation_cannot_be_excluded_or_replaced(self, path):
        for replacement in (None, "header.go"):
            for key in ("allowedProductionAdaptations", "adaptedCandidateFiles"):
                with self.subTest(path=path, replacement=replacement, key=key):
                    manifest = copy.deepcopy(self.manifest)
                    if key == "allowedProductionAdaptations":
                        sources = manifest[key].pop(path)
                        if replacement is not None:
                            manifest[key][replacement] = sources
                    else:
                        index = next(i for i, item in enumerate(manifest[key])
                                     if item["path"] == path)
                        item = manifest[key].pop(index)
                        self.assertEqual(item["path"], path)
                        if replacement is not None:
                            item["path"] = replacement
                            manifest[key].append(item)
                    with self.assertRaisesRegex(SystemExit, "source/per-file85 partition"):
                        CHECKER.production_adaptation_paths(manifest)

    def test_wrapper_cannot_be_excluded_or_replaced_with_unadapted_original(self):
        self.assert_adaptation_cannot_be_excluded_or_replaced("client_wrapper.go")

    def test_bisect_cannot_be_excluded_or_replaced_with_unadapted_original(self):
        self.assert_adaptation_cannot_be_excluded_or_replaced("internal/bisect/bisect.go")

    def test_original_wrapper_provenance_and_original16_order_remain(self):
        paths = CHECKER.production_adaptation_paths(self.manifest)
        self.assertEqual(paths[:16], (
            "internal/http2/flow.go", "internal/http2/transport.go", "internal/http2/frame.go",
            "internal/dump/dump.go", "request.go", "response.go", "trace.go", "internal/http3/transport.go",
            "internal/http3/stream.go", "internal/compress/deflate_reader.go", "decode.go", "digest.go",
            "parallel_download.go", "client.go", "dump.go", "transport.go"))
        original = next(item for item in self.manifest["originalFiles"]
                        if item["path"] == "client_wrapper.go")
        self.assertEqual(original, {"path": "client_wrapper.go", "size": 30282,
                         "sha256": "ca330cd036d9cc0936ad5f0170dff3be7e4ec814b9a8c53b9259c89f1a1be0dc"})
        self.assertNotIn("client_wrapper.go", self.manifest["newOwnedFiles"])
        self.assertEqual(132 - len(paths) - len(self.manifest["originalTestAdaptations"]), 112)

    def test_original_bisect_provenance_and_original17_order_remain(self):
        paths = CHECKER.production_adaptation_paths(self.manifest)
        self.assertEqual(paths[16:], ("client_wrapper.go", "internal/bisect/bisect.go"))
        self.assertEqual(tuple(self.manifest["allowedProductionAdaptations"]), paths)
        self.assertEqual(tuple(item["path"] for item in self.manifest["adaptedCandidateFiles"]), paths)
        original = next(item for item in self.manifest["originalFiles"]
                        if item["path"] == "internal/bisect/bisect.go")
        self.assertEqual(original, {"path": "internal/bisect/bisect.go", "size": 23969,
                         "sha256": "de2e6582ffb06e9745167ae4ed820371b88c7b3af5a793d289a9997ebaedf916"})
        self.assertNotIn("internal/bisect/bisect.go", self.manifest["newOwnedFiles"])


class SourcePartitionRegistrationRegression(unittest.TestCase):
    """CI-only isolated source/manifest controls; simulated go-list, never Go PASS."""

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="req-partition-verifier-")
        self.addCleanup(self.temporary.cleanup)
        root = Path(self.temporary.name)
        self.backend = root / "backend"
        self.controlled = self.backend / "third_party/req"
        shutil.copytree(HERE.parent / "third_party/req", self.controlled)
        self.evidence = root / "evidence"
        self.evidence.mkdir()
        self.manifest_path = self.controlled / "UPSTREAM-MANIFEST.json"
        self.manifest = json.loads(self.manifest_path.read_text())
        self.wrapper_bytes = (self.controlled / "client_wrapper.go").read_bytes()
        self.bisect_bytes = (self.controlled / "internal/bisect/bisect.go").read_bytes()
        self.modules = [{"Path": CHECKER.MODULE, "Version": "v3.57.0",
                         "Replace": {"Dir": str(self.controlled)}}]
        # This test only simulates source-selection metadata. The actual CI
        # command still supplies independently captured real go-list output.
        by_directory = {}
        for path in self.controlled.rglob("*_test.go"):
            relative = path.relative_to(self.controlled)
            if relative.as_posix() in ("internal/testdata/cert_test.go",
                                       "internal/testdata/testdata_suite_test.go"):
                continue
            by_directory.setdefault(relative.parent.as_posix(), []).append(relative.name)
        self.packages = [{"ImportPath": CHECKER.MODULE + ("/" + name if name != "." else ""),
                          "Dir": str(self.controlled / name), "TestGoFiles": sorted(names)}
                         for name, names in sorted(by_directory.items())]

    def verify(self):
        def simulated_objects(path):
            self.assertIn(path.name, ("modules.json", "packages.json"))
            return copy.deepcopy(self.modules if path.name == "modules.json" else self.packages)
        with mock.patch.object(CHECKER, "objects", side_effect=simulated_objects), \
             mock.patch.object(CHECKER.sys, "argv", ["checker", "packages", str(self.backend), str(self.evidence)]):
            CHECKER.main()

    def test_actual_source_copy_satisfies_simulated_selection_shape_only(self):
        self.verify()
        proof = json.loads((self.evidence / "package-selection-proof.json").read_text())
        self.assertEqual(len(proof["originalSelected"]), 12)
        self.assertEqual(len(proof["originalOmitted"]), 2)
        self.assertEqual(proof["originalTestAdaptations"], self.manifest["originalTestAdaptations"])

    def test_wrong_partition_wrapper_hash_ownership_or_source_rejects(self):
        cases = [("unmodified113", None, "Retain exactly112 unchanged originals"),
                 ("unmodified114", None, "Retain exactly112 unchanged originals")]
        for path in ("client_wrapper.go", "internal/bisect/bisect.go"):
            cases.extend((mutation, path, diagnostic) for mutation, diagnostic in (
                ("hash", "Adapted source hash mismatch: " + path),
                ("bytes", "Adapted source hash mismatch: " + path),
                ("excluded", "source/per-file85 partition"),
                ("new_owned", "exactly132 originals")))
        for mutation, path, diagnostic in cases:
            with self.subTest(mutation=mutation, path=path):
                manifest = copy.deepcopy(self.manifest)
                (self.controlled / "client_wrapper.go").write_bytes(self.wrapper_bytes)
                (self.controlled / "internal/bisect/bisect.go").write_bytes(self.bisect_bytes)
                if mutation in ("unmodified113", "unmodified114"):
                    manifest["originalUnmodifiedFileCount"] = int(mutation.removeprefix("unmodified"))
                elif mutation == "hash":
                    item = next(item for item in manifest["adaptedCandidateFiles"]
                                if item["path"] == path)
                    item["sha256"] = "0" * 64
                elif mutation == "bytes":
                    original = self.wrapper_bytes if path == "client_wrapper.go" else self.bisect_bytes
                    (self.controlled / path).write_bytes(original + b"\n")
                elif mutation == "excluded":
                    del manifest["allowedProductionAdaptations"][path]
                    index = next(i for i, item in enumerate(manifest["adaptedCandidateFiles"])
                                 if item["path"] == path)
                    manifest["adaptedCandidateFiles"].pop(index)
                else:
                    manifest["newOwnedFiles"].append(path)
                self.manifest_path.write_text(json.dumps(manifest, indent=2) + "\n")
                proof = self.evidence / "package-selection-proof.json"
                proof.unlink(missing_ok=True)
                with self.assertRaisesRegex(SystemExit, diagnostic):
                    self.verify()
                self.assertFalse(proof.exists(), "Rejected partition must not publish complete proof")


if __name__ == "__main__":
    unittest.main(verbosity=2)
