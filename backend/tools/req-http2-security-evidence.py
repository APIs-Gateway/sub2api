#!/usr/bin/env python3
"""Fail closed on actual remote test events, source identity and coverage.

This tool is executed by remote CI. It never supplies synthetic test events or
coverage hits. Original profiles and every count are preserved as artifacts.
"""
import difflib
import hashlib
import io
import json
from pathlib import Path
import re
import subprocess
import sys
import urllib.request
import zipfile

MODULE = "github.com/imroc/req/v3"
ZIP_SHA = "7551b781c424d09cbd5823a0d5f9ff7dc5002aadd9ed226a776aa7150a2d186c"


def require(condition, message):
    if not condition:
        raise SystemExit(message)


def objects(path):
    text = path.read_text(encoding="utf-8")
    decoder = json.JSONDecoder()
    result = []
    while text.strip():
        text = text.lstrip()
        value, offset = decoder.raw_decode(text)
        result.append(value)
        text = text[offset:]
    return result


def report(evidence, name, value):
    (evidence / name).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def events(path):
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()]


def successful_events(path):
    values = events(path)
    require(all(isinstance(value, dict) and
                isinstance(value.get("Package"), str) and value["Package"] and
                ("Test" not in value or
                 (isinstance(value["Test"], str) and value["Test"]))
                for value in values), "Malformed package/test event identity")
    # Go emits a package-level skip for packages without test files. It is
    # never a PASS claim; actual test-level skips remain a blocking failure.
    require(not any(v.get("Action") == "fail" or
                    (v.get("Action") == "skip" and "Test" in v) for v in values),
            f"Failed/skipped test event in {path.name}")
    packages = {v["Package"] for v in values if v.get("Action") == "pass" and "Test" not in v}
    require(packages, "No actual package completion events")
    return values, packages


def inventory(backend):
    return json.loads((backend / "tools/req-http2-security-inventory.json").read_text(encoding="utf-8"))


def verify_named(backend, evidence, filename):
    expected = inventory(backend)
    values, packages = successful_events(evidence / filename)
    default_tls = {
        "TestReqHTTP2Security_DefaultTLSCallbacks/explicit_server_name",
        "TestReqHTTP2Security_DefaultTLSCallbacks/inferred_server_name",
        "TestReqHTTP2Security_DefaultTLSCallbacks/nil_config_certificate_error",
        "TestReqHTTP2Security_DefaultTLSCallbacks/canceled_handshake",
    }
    actual_default_tls = {name for name in expected["privateLeaves"]
                          if name.startswith("TestReqHTTP2Security_DefaultTLSCallbacks/")}
    require(expected["privateCount"] == 41 and expected["publicCount"] == 4 and
            actual_default_tls == default_tls and
            len(set(expected["privateLeaves"]) - default_tls) == 37,
            "Retain37 original private+4 public HTTP2 leaves and4 default TLS leaves")
    proof = []
    original_kinds = ("private", "public", "dump", "trace", "dumpPublic", "http1", "proxy")
    require(sum(expected[kind + "Count"] for kind in original_kinds) == 97,
            "Retain all97 original named HTTP2/dump/trace/HTTP1/proxy leaves")
    require(expected["http3ContractCount"] == 55,
            "Retain all55 first privateHTTP3 parser/writer contract leaves")
    require(expected["http3WireCount"] == 6,
            "Retain all6 original public HTTP3 wire contract leaves")
    require(expected["http3PublicTLSCount"] == 6 and expected["http3TLSControlCount"] == 5,
            "Retain all11 public and private HTTP3 TLS controls")
    require(expected["http3DecompressionCount"] == 11,
            "Require all11 public HTTP3 body/decompression and opaque coding controls")
    require(expected["deflateCloseWireCount"] == 2 and expected["deflateCloseUnitCount"] == 7,
            "Require both real HTTP3 deflate close cases plus7 close/error unit controls")
    retained_kinds = original_kinds + ("http3Contract", "http3Wire", "http3PublicTLS", "http3TLSControl", "http3Decompression",
                                       "deflateCloseWire", "deflateCloseUnit", "http2FrameContract")
    require(sum(expected[kind + "Count"] for kind in retained_kinds) == 338,
            "Retain all338 reviewed baseline leaves")
    require(expected["http2FrameContractCount"] == 149 and expected["http1TransferContractCount"] == 30 and
            expected["http2TransportContractCount"] == 25 and expected["metaCharsetCount"] == 8,
            "Retain all393 reviewed leaves plus8 actual HTTP charset leaves")
    baseline_kinds = retained_kinds + ("http1TransferContract", "http2TransportContract", "metaCharset")
    require(sum(expected[kind + "Count"] for kind in baseline_kinds) == 401,
            "Retain every original401 mandatory leaf")
    require(expected["clientBehaviorCount"] == 44 and expected["digestOwnershipCount"] == 23 and
            sum(expected[kind + "Count"] for kind in baseline_kinds + ("clientBehavior", "digestOwnership")) == 468,
            "Retain reviewed F44 and all23 Digest ownership leaves with original401")
    require(expected["downloadContractCount"] == 32 and expected["parallelDownloadFallbackCount"] == 1 and
            expected["parallelDownloadHTTP3CleanupCount"] == 1 and expected["parallelDownloadInProcessCount"] == 23 and
            expected["parallelDownloadDefaultOutputCount"] == 1 and expected["http2ReceiveFlowCount"] == 6 and
            expected["dumpOwnershipCount"] == 42,
            "Retain all526 original G leaves, six HTTP2 receive-flow and42 dump ownership leaves")
    require(expected["customTLSCount"] == 18 and expected["customTLSHTTP2Count"] == 13 and
            expected["customTLSPackage"] == MODULE and
            expected["customTLSHTTP2Package"] == MODULE + "/internal/http2",
            "Retain all574 prior leaves plus18 root and13 private HTTP2 custom TLS leaves")
    legacy_kinds = baseline_kinds + ("clientBehavior", "digestOwnership", "downloadContract", "parallelDownloadFallback", "parallelDownloadHTTP3Cleanup", "parallelDownloadInProcess", "parallelDownloadDefaultOutput", "http2ReceiveFlow", "dumpOwnership", "customTLS", "customTLSHTTP2")
    require(sum(expected[kind + "Count"] for kind in legacy_kinds) == 605,
            "Retain every original605 mandatory leaf")
    expansion = (("publicAPI", MODULE, 70), ("http1Boundary", MODULE, 33),
                 ("publicDefault", MODULE, 6), ("bisectContract", MODULE + "/internal/bisect", 112),
                 ("http2Coverage", MODULE + "/internal/http2", 51), ("safariWrapper", MODULE, 3))
    for kind, package, count in expansion:
        require(expected[kind + "Package"] == package and expected[kind + "Count"] == count,
                "Invalid expansion registry: " + kind)
    kinds = legacy_kinds + tuple(kind for kind, _, _ in expansion)
    require(expected["totalNamedLeaves"] == 880,
            "Require original605 plus275 reviewed expansion leaves")
    require(expected["totalNamedLeaves"] == sum(expected[kind + "Count"] for kind in kinds),
            "Incomplete total named leaf inventory")
    # Preserve event order within each actual identity while avoiding repeated
    # full-stream scans by the CI-only missing/duplicate verifier controls.
    by_identity = {}
    for value in values:
        if "Test" in value:
            by_identity.setdefault((value["Package"], value["Test"]), []).append(value)
    for kind in kinds:
        package = expected[kind + "Package"]
        leaves = expected[kind + "Leaves"]
        require(len(leaves) == expected[kind + "Count"] and leaves and len(leaves) == len(set(leaves)), "Invalid inventory")
        require(package in packages, f"Missing package PASS: {package}")
        require(sum(v.get("Action") == "pass" and v.get("Package") == package and "Test" not in v
                    for v in values) == 1, f"Missing/duplicate package PASS: {package}")
        for test in leaves:
            actual = by_identity.get((package, test), [])
            require(sum(v.get("Action") == "run" for v in actual) == 1, f"Missing/duplicate RUN {test}")
            require(sum(v.get("Action") == "pass" for v in actual) == 1, f"Missing/duplicate PASS {test}")
            if kind == "parallelDownloadInProcess":
                output = "".join(v.get("Output", "") for v in actual)
                require(output.count("G_IN_PROCESS_EXECUTED " + test + "\n") == 1 and
                        output.count("G_IN_PROCESS_BODY_DONE " + test + "\n") == 1,
                        f"Missing/duplicate actual parent execution markers: {test}")
                require("G_CHILD_ACTUAL" not in output and "G_CHILD_EXECUTED" not in output,
                        f"Parent contract incorrectly delegated to child: {test}")
            proof.append({"package": package, "test": test, "actions": [v["Action"] for v in actual]})
    require(len(proof) == len({(item["package"], item["test"]) for item in proof}) == expected["totalNamedLeaves"],
            "Duplicate registered package/test identity")
    # Detect new security leaves accidentally omitted from the frozen inventory.
    prefixes = ("TestReqHTTP2Security_", "TestDumper", "TestReqTraceSecurity_", "TestReqDumpSecurity_",
                "TestReqHTTP1Lifecycle_", "TestReqProxyLifecycle_", "TestReqHTTP3Contract_", "TestReqHTTP3Wire_",
                "TestReqHTTP3PublicTLS_", "TestReqHTTP3TLSControl_", "TestReqHTTP3Decompression_",
                "TestReqHTTP3DeflateClose_", "TestReqDeflateClose_", "TestReqHTTP2FrameContract_", "TestReqHTTP1Transfer_",
                "TestReqHTTP2TransportContract_", "TestReqMetaCharset_", "TestReqClientDigest_", "TestReqClientRedirect_",
                "TestReqClientCookie_", "TestReqClientResponse_", "TestReqDigestOwnership_",
                "TestReqDownloadContract", "TestReqParallelDownloadContract", "TestReqParallelDownloadFallback",
                "TestReqParallelDownloadHTTP3Cleanup", "TestReqParallelDownloadInProcessContract",
                "TestReqParallelDownloadDefaultOutput", "TestReqHTTP2ReceiveFlowContract_", "TestReqDumpOwnership", "TestReqCustomTLS_",
                "TestReqPublicAPI_", "TestReqH1Boundary_", "TestReqPublicDefaultContract", "TestReqBisectContract_",
                "TestReqHTTP2CoverageContract_", "TestReqHTTP2CoverageFrame_", "TestReqSafariWrapperContract")
    runs = {(v.get("Package"), v["Test"]) for v in values if v.get("Action") == "run" and v.get("Test", "").startswith(prefixes)}
    leaves = {key for key in runs if not any(other[0] == key[0] and other[1].startswith(key[1] + "/") for other in runs)}
    require(leaves == {(v["package"], v["test"]) for v in proof}, "Actual security leaf inventory differs")
    report(evidence, filename + ".inventory-proof.json", proof)
    return packages


def verify_adapted_original_tests(backend, values):
    """Require actual completion of every retained original adaptation contract."""
    expected = {
        "request_test.go": ["TestSetFile", "TestTraceInfo", "TestEnableDumpToFIle"],
        "client_test.go": ["TestEnableDumpAllToFile"],
    }
    manifest = json.loads((backend / "third_party/req/UPSTREAM-MANIFEST.json").read_text(encoding="utf-8"))
    declared = {item["path"]: item["requiredTests"] for item in manifest["originalTestAdaptations"]}
    require(declared == expected, "Original adapted required test inventory changed")
    proof = []
    for path, tests in expected.items():
        for test in tests:
            actual = [value for value in values if value.get("Package") == MODULE and value.get("Test") == test]
            require(sum(value.get("Action") == "run" for value in actual) == 1,
                    "Missing/duplicate original RUN " + test)
            require(sum(value.get("Action") == "pass" for value in actual) == 1 and
                    not any(value.get("Action") in ("fail", "skip") for value in actual),
                    "Missing/duplicate or failed original PASS " + test)
            proof.append({"path": path, "package": MODULE, "test": test,
                          "actions": [value["Action"] for value in actual]})
    return proof



def compare_old_module_graphs(current, old, backend, evidence):
    """Compare all MVS fields except proven modfile/req and optional cache paths."""
    for values in (current, old):
        require(len(values) == 365, "Incomplete secured 365-module graph")
        paths = [value.get("Path") for value in values]
        require(all(isinstance(path, str) and path for path in paths) and
                len(paths) == len(set(paths)), "Empty/duplicate module paths")
        require(sum(value.get("Main") is True for value in values) == 1,
                "Expected exactly one main module")
        require(sum(value["Path"] == MODULE for value in values) == 1,
                "Expected exactly one controlled req module")
    require([value["Path"] for value in current] == [value["Path"] for value in old],
            "Old-red module path/order changed")
    cache_changes = []

    def normalized(value, replacement, replacement_path, main_modfile):
        result = dict(value)
        if value.get("Main"):
            require("Replace" not in value, "Unexpected main replacement")
            require(Path(value["GoMod"]).resolve() == main_modfile,
                    "Unexpected main modfile")
            result.pop("GoMod")
        elif value["Path"] == MODULE:
            require(Path(value["Dir"]).resolve() == replacement and
                    Path(value["GoMod"]).resolve() == replacement / "go.mod",
                    "Incorrect controlled req source directory/modfile")
            replace = dict(value["Replace"])
            require(replace["Path"] == replacement_path and
                    Path(replace["Dir"]).resolve() == replacement and
                    Path(replace["GoMod"]).resolve() == replacement / "go.mod",
                    "Incorrect old/current replacement")
            for key in ("Path", "Dir", "GoMod"):
                replace.pop(key)
            # Preserve every non-location replacement field, e.g. GoVersion.
            result["Replace"] = replace
            result.pop("Dir")
            result.pop("GoMod")
        else:
            require(isinstance(value.get("Version"), str) and value["Version"],
                    "External module is not versioned")
            require("Replace" not in value, "Unexpected external replacement")
            if "Dir" in value:
                require(isinstance(value["Dir"], str) and value["Dir"] and
                        Path(value["Dir"]).is_absolute(), "Invalid external cache Dir")
            result.pop("Dir", None)
        return result

    for before, after in zip(current, old):
        if not before.get("Main") and before["Path"] != MODULE:
            if "Dir" in before and "Dir" in after:
                require(before["Dir"] == after["Dir"], "External cache Dir moved")
            elif ("Dir" in before) != ("Dir" in after):
                cache_changes.append({"path": before["Path"],
                                      "currentDir": before.get("Dir"),
                                      "oldDir": after.get("Dir")})
        require(normalized(before, backend / "third_party/req", "./third_party/req",
                           backend / "go.mod") ==
                normalized(after, evidence / "original", str(evidence / "original"),
                           evidence / "old.mod"), "Old-red MVS changed")
    return {"identicalModules": len(old),
            "sourceLocationChange": "req replacement and main modfile locations only",
            "optionalExternalCacheDirChanges": cache_changes,
            "allOtherModuleAndReplacementFieldsIdentical": True}


def main():
    operation, source, destination = sys.argv[1:]
    backend, evidence = Path(source).resolve(), Path(destination).resolve()
    if operation == "inputs":
        require(not evidence.is_relative_to(backend), "Evidence must be outside the frozen checkout")
        for options in (["--others", "--exclude-standard"],
                        ["--others", "--ignored", "--exclude-standard"]):
            extra = subprocess.check_output(["git", "ls-files", "-z"] + options, cwd=backend)
            require(not extra, "Untracked/ignored input outside frozen HEAD: " + repr(extra))
        paths = subprocess.check_output(["git", "ls-files", "-z"], cwd=backend).split(b"\0")
        proof = []
        for raw in filter(None, paths):
            name = raw.decode()
            blob = subprocess.check_output(["git", "rev-parse", "HEAD:" + name], cwd=backend).decode().strip()
            actual = subprocess.check_output(["git", "hash-object", "--", name], cwd=backend).decode().strip()
            require(actual == blob, "Frozen input differs: " + name)
            proof.append({"path": name, "gitBlob": blob, "sha256": hashlib.sha256((backend / name).read_bytes()).hexdigest()})
        require(any(v["path"].startswith("frontend/") for v in proof), "Full repository/frontend fixtures missing")
        report(evidence, "full-repository-inputs.json", proof)
    elif operation == "packages":
        modules = objects(evidence / "modules.json")
        req = [v for v in modules if v["Path"] == MODULE]
        controlled = backend / "third_party/req"
        require(len(req) == 1 and req[0]["Version"] == "v3.57.0", "Wrong req version")
        require(Path(req[0]["Replace"]["Dir"]).resolve() == controlled, "Wrong module replacement")
        packages = objects(evidence / "packages.json")
        selected = set()
        for package in packages:
            relative = package["ImportPath"].removeprefix(MODULE).lstrip("/")
            require(Path(package["Dir"]).resolve() == controlled / relative, "Wrong package directory")
            for name in package.get("TestGoFiles", []) + package.get("XTestGoFiles", []):
                selected.add((Path(relative) / name).as_posix())
        manifest = json.loads((controlled / "UPSTREAM-MANIFEST.json").read_text(encoding="utf-8"))
        expected_paths = {v["path"] for v in manifest["originalFiles"]} | set(manifest["newOwnedFiles"])
        actual_paths = set()
        for path in controlled.rglob("*"):
            require(not path.is_symlink(), "Unexpected controlled module symlink")
            if path.is_file():
                actual_paths.add(path.relative_to(controlled).as_posix())
        require(len(manifest["originalFiles"]) == 132 and
                len(expected_paths) == 132 + len(manifest["newOwnedFiles"]) and
                actual_paths == expected_paths,
                "Controlled module must contain exactly132 originals+declared owned files")
        adaptations = set(production_adaptation_paths(manifest))
        require(len(manifest["adaptedCandidateFiles"]) == 18 and
                {v["path"] for v in manifest["adaptedCandidateFiles"]} == adaptations,
                "Incomplete adapted source hash inventory")
        production = {v["path"] for v in manifest["originalFiles"]
                      if v["path"].endswith(".go") and not v["path"].endswith("_test.go")}
        require(len(production) == 89 and adaptations <= production,
                "Retain all89 original production Go files and exactly18 declared adaptations")
        test_adaptations = manifest.get("originalTestAdaptations", [])
        require(isinstance(test_adaptations, list) and len(test_adaptations) == 2 and
                {item.get("path") for item in test_adaptations} == {"request_test.go", "client_test.go"},
                "Original test adaptation registration differs")
        original_by_path = {item["path"]: item for item in manifest["originalFiles"]}
        adapted_tests = {item["path"] for item in test_adaptations}
        for item in test_adaptations:
            path = item["path"]
            original = original_by_path[path]
            require(item["originalBytes"] == original["size"] and
                    item["originalSHA256"] == original["sha256"],
                    "Adapted original test archive anchor mismatch: " + path)
            require(path in selected, "Adapted original test file not selected: " + path)
            data = (controlled / path).read_bytes()
            require(len(data) == item["candidateBytes"] and
                    hashlib.sha256(data).hexdigest() == item["candidateSHA256"],
                    "Adapted original test hash mismatch: " + path)
        require(manifest["originalUnmodifiedFileCount"] == 112 and
                len(original_by_path) - len(adaptations) - len(adapted_tests) == 112,
                "Retain exactly112 unchanged originals after18 production and two declared test adaptations")
        for item in manifest["originalFiles"]:
            if item["path"] not in adaptations | adapted_tests:
                data = (controlled / item["path"]).read_bytes()
                require(len(data) == item["size"] and hashlib.sha256(data).hexdigest() == item["sha256"], "Original source modified: " + item["path"])
        for item in manifest["adaptedCandidateFiles"]:
            data = (controlled / item["path"]).read_bytes()
            require(len(data) == item["bytes"] and hashlib.sha256(data).hexdigest() == item["sha256"], "Adapted source hash mismatch: " + item["path"])
        originals = {v["path"] for v in manifest["originalFiles"] if v["path"].endswith("_test.go")}
        omitted = {"internal/testdata/cert_test.go", "internal/testdata/testdata_suite_test.go"}
        require(len(originals) == 14 and originals - selected == omitted, "Original wildcard test selection changed")
        require(len(originals & selected) == 12, "Expected 12 selected original test files")
        report(evidence, "package-selection-proof.json", {"module": req[0], "selectedTests": sorted(selected), "originalSelected": sorted(originals & selected), "originalOmitted": sorted(omitted), "originalTestAdaptations": test_adaptations, "omission": "Original testdata helper suite is excluded by Go wildcard; cert_test.go lacks io import. No PASS claimed."})
    elif operation == "named":
        verify_named(backend, evidence, "named.json")
    elif operation == "all":
        passed = verify_named(backend, evidence, "all.json")
        packages = objects(evidence / "packages.json")
        expected = {v["ImportPath"] for v in packages if v.get("TestGoFiles") or v.get("XTestGoFiles")}
        require(expected <= passed, "Original package tests did not complete: " + repr(sorted(expected - passed)))
        actual_events = events(evidence / "all.json")
        original_test_proof = verify_adapted_original_tests(backend, actual_events)
        skips = {v["Package"] for v in actual_events if v.get("Action") == "skip" and "Test" not in v}
        require(not skips & expected, "Test-bearing package skipped")
        report(evidence, "all-package-completion-proof.json", {"testPackagesPassed": sorted(expected), "noTestFilePackagesSkipped": sorted(skips), "adaptedOriginalTestCompletions": original_test_proof})
    elif operation == "original":
        with urllib.request.urlopen("https://proxy.golang.org/github.com/imroc/req/v3/@v/v3.57.0.zip", timeout=60) as response:
            data = response.read()
        require(hashlib.sha256(data).hexdigest() == ZIP_SHA, "Official archive SHA mismatch")
        (evidence / "original.zip").write_bytes(data)
        prefix = MODULE + "@v3.57.0/"
        manifest = json.loads((backend / "third_party/req/UPSTREAM-MANIFEST.json").read_text(encoding="utf-8"))
        expected = {v["path"]: v for v in manifest["originalFiles"]}
        seen = []
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            for item in archive.infolist():
                if item.is_dir():
                    continue
                require(item.filename.startswith(prefix), "Unexpected archive prefix")
                name = item.filename[len(prefix):]
                require(name in expected and name not in seen and not Path(name).is_absolute() and ".." not in Path(name).parts, "Unexpected/duplicate archive path")
                value = archive.read(item)
                require(len(value) == expected[name]["size"] and hashlib.sha256(value).hexdigest() == expected[name]["sha256"], "Original file mismatch: " + name)
                target = evidence / "original" / name
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(value)
                seen.append(name)
        require(set(seen) == set(expected) and len(seen) == 132, "Incomplete old source")
        report(evidence, "original-source-proof.json", {"archiveSHA256": ZIP_SHA, "verifiedFiles": expected})
    elif operation == "old-graph":
        original = objects(evidence / "modules.json")
        old = objects(evidence / "old-modules.json")
        graph_proof = compare_old_module_graphs(original, old, backend, evidence)
        current = (backend / "go.mod").read_text(encoding="utf-8")
        oldmod = (evidence / "old.mod").read_text(encoding="utf-8")
        expected = current.replace("replace " + MODULE + " => ./third_party/req", "replace " + MODULE + " => " + str(evidence / "original"))
        require(oldmod == expected, "Old modfile changed more than replace path")
        require((backend / "go.sum").read_bytes() == (evidence / "old.sum").read_bytes(), "Old-red sums changed")
        graph_proof["rawGraphSHA256"] = {name: hashlib.sha256((evidence / name).read_bytes()).hexdigest()
                                      for name in ("modules.json", "old-modules.json")}
        graph_proof["oldModfileOnlyReqReplaceChanged"] = True
        graph_proof["oldSumBytesIdentical"] = True
        report(evidence, "old-graph-proof.json", graph_proof)
    elif operation == "old-red":
        expected = inventory(backend)
        values = events(evidence / "old-red.json")
        package, test = expected["privatePackage"], expected["sameOldWireWitness"]
        require((evidence / "old-red.exit").read_text(encoding="utf-8").strip() == "1", "Old witness needs actual assertion failure exit1")
        matching = [v for v in values if v.get("Package") == package and v.get("Test") == test]
        require(any(v.get("Action") == "run" for v in matching) and any(v.get("Action") == "fail" for v in matching), "Old witness did not run/fail exact test")
        output = "".join(v.get("Output", "") for v in matching)
        require(expected["oldWitnessExpectedAssertionMarker"] in output, "Old failure lacks wrong-DATA assertion")
        require(not any(v.get("Action") == "pass" for v in matching), "Old witness unexpectedly passed")
        fixture = Path("internal/http2/transport_security_test.go")
        original_fixture = (backend / "third_party/req" / fixture).read_bytes()
        require(original_fixture == (evidence / "original" / fixture).read_bytes(), "Old wire fixture differs")
        report(evidence, "old-red-proof.json", {"package": package, "test": test, "marker": expected["oldWitnessExpectedAssertionMarker"], "actualExit": 1, "sameWireFixtureSHA256": hashlib.sha256(original_fixture).hexdigest()})
    elif operation == "coverage":
        coverage(backend, evidence)
    else:
        raise SystemExit("Unknown evidence operation")


def production_adaptation_paths(manifest):
    """Share the exact source partition between identity and per-file85 gates."""
    expected = ("internal/http2/flow.go", "internal/http2/transport.go", "internal/http2/frame.go",
                "internal/dump/dump.go", "request.go", "response.go", "trace.go", "internal/http3/transport.go",
                "internal/http3/stream.go", "internal/compress/deflate_reader.go", "decode.go", "digest.go",
                "parallel_download.go", "client.go", "dump.go", "transport.go",
                "client_wrapper.go", "internal/bisect/bisect.go")
    # User authorized the specific global Safari wrapper correction in existing
    # PR1712 on2026-10-29. Keep all132 archive rows and original16 adaptations;
    # the wrapper retains index16. The user authorized the reproduced private
    # bisect cache race repair on2026-10-30: append it at index17 and apply
    # the same actual fix-block85 gate to all18 production adaptations.
    declared = manifest["allowedProductionAdaptations"]
    candidates = [item["path"] for item in manifest["adaptedCandidateFiles"]]
    require(isinstance(declared, dict) and len(declared) == len(candidates) == len(expected) == 18 and
            set(declared) == set(candidates) == set(expected),
            "Unexpected or missing production adaptation in source/per-file85 partition")
    return expected


def coverage(backend, evidence):
    proofs = []
    blocks = {}
    pattern = re.compile(r"(.+):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)")
    for kind in ("named", "all"):
        raw = (evidence / (kind + ".raw.out")).read_text(encoding="utf-8").splitlines()
        require(raw and raw[0] == "mode: atomic", "Missing atomic profile")
        mapped = [raw[0]]
        for line in raw[1:]:
            match = pattern.fullmatch(line)
            require(match is not None and match[1].startswith(MODULE + "/"), "Unexpected raw coverprofile path")
            path = "backend/third_party/req/" + match[1][len(MODULE) + 1:]
            require((backend.parent / path).is_file(), "Missing mapped source")
            suffix = line[len(match[1]):]
            mapped.append(path + suffix)
            proofs.append({"profile": kind, "rawPath": match[1], "mappedPath": path, "positionStatementsCount": suffix})
            if kind == "all":
                key = (path, int(match[2]), int(match[3]), int(match[4]), int(match[5]))
                statements, count = int(match[6]), int(match[7])
                if key in blocks:
                    require(blocks[key][0] == statements, "Duplicate block statements differ")
                    blocks[key][1] += count
                else:
                    blocks[key] = [statements, count]
        (evidence / (kind + ".mapped.out")).write_text("\n".join(mapped) + "\n", encoding="utf-8")
    report(evidence, "coverage-path-only-proof.json", proofs)
    result = []
    manifest = json.loads((backend / "third_party/req/UPSTREAM-MANIFEST.json").read_text(encoding="utf-8"))
    adapted = production_adaptation_paths(manifest)
    for relative in adapted:
        old = (evidence / "original" / relative).read_text(encoding="utf-8").splitlines()
        new = (backend / "third_party/req" / relative).read_text(encoding="utf-8").splitlines()
        added = set()
        for tag, _, _, start, end in difflib.SequenceMatcher(None, old, new, autojunk=False).get_opcodes():
            if tag in ("insert", "replace"):
                added.update(range(start + 1, end + 1))
        path = "backend/third_party/req/" + relative
        selected = []
        for key, (statements, count) in blocks.items():
            # End position is exclusive; a column-1 end excludes that line.
            end = key[3] - (key[4] == 1)
            if key[0] == path and statements > 0 and any(key[1] <= line <= end for line in added):
                selected.append({"block": key, "statements": statements, "count": count})
        total = sum(v["statements"] for v in selected)
        hit = sum(v["statements"] for v in selected if v["count"] > 0)
        result.append({"path": path, "addedLines": sorted(added), "blocks": selected, "coveredStatements": hit, "statements": total, "percent": 100 * hit / total if total else 0})
    report(evidence, "official-and-local-adapted-fix-block-coverage.json", result)
    require(all(v["statements"] > 0 and 100 * v["coveredStatements"] >= 85 * v["statements"] for v in result), "Actual added fix-block coverage below85; Codecov85 also remains required")


if __name__ == "__main__":
    main()
