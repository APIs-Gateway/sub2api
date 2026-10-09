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
    text = path.read_text()
    decoder = json.JSONDecoder()
    result = []
    while text.strip():
        text = text.lstrip()
        value, offset = decoder.raw_decode(text)
        result.append(value)
        text = text[offset:]
    return result


def report(evidence, name, value):
    (evidence / name).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def events(path):
    return [json.loads(line) for line in path.read_text().splitlines()]


def successful_events(path):
    values = events(path)
    # Go emits a package-level skip for packages without test files. It is
    # never a PASS claim; actual test-level skips remain a blocking failure.
    require(not any(v.get("Action") == "fail" or
                    (v.get("Action") == "skip" and "Test" in v) for v in values),
            f"Failed/skipped test event in {path.name}")
    packages = {v["Package"] for v in values if v.get("Action") == "pass" and "Test" not in v}
    require(packages, "No actual package completion events")
    return values, packages


def inventory(backend):
    return json.loads((backend / "tools/req-http2-security-inventory.json").read_text())


def verify_named(backend, evidence, filename):
    expected = inventory(backend)
    values, packages = successful_events(evidence / filename)
    proof = []
    for kind in ("private", "public"):
        package = expected[kind + "Package"]
        leaves = expected[kind + "Leaves"]
        require(len(leaves) == expected[kind + "Count"] and len(leaves) == len(set(leaves)), "Invalid inventory")
        require(package in packages, f"Missing package PASS: {package}")
        for test in leaves:
            actual = [v for v in values if v.get("Package") == package and v.get("Test") == test]
            require(sum(v.get("Action") == "run" for v in actual) == 1, f"Missing/duplicate RUN {test}")
            require(sum(v.get("Action") == "pass" for v in actual) == 1, f"Missing/duplicate PASS {test}")
            proof.append({"package": package, "test": test, "actions": [v["Action"] for v in actual]})
    # Detect new security leaves accidentally omitted from the frozen inventory.
    runs = {(v.get("Package"), v["Test"]) for v in values if v.get("Action") == "run" and v.get("Test", "").startswith("TestReqHTTP2Security_")}
    leaves = {key for key in runs if not any(other[0] == key[0] and other[1].startswith(key[1] + "/") for other in runs)}
    require(leaves == {(v["package"], v["test"]) for v in proof}, "Actual security leaf inventory differs")
    report(evidence, filename + ".inventory-proof.json", proof)
    return packages


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
                selected.add(str(Path(relative) / name))
        manifest = json.loads((controlled / "UPSTREAM-MANIFEST.json").read_text())
        expected_paths = {v["path"] for v in manifest["originalFiles"]} | set(manifest["newOwnedFiles"])
        actual_paths = set()
        for path in controlled.rglob("*"):
            require(not path.is_symlink(), "Unexpected controlled module symlink")
            if path.is_file():
                actual_paths.add(path.relative_to(controlled).as_posix())
        require(len(expected_paths) == 139 and actual_paths == expected_paths,
                "Controlled module must contain exactly132 originals+7 declared files")
        adaptations = set(manifest["allowedProductionAdaptations"])
        require(adaptations == {"internal/http2/flow.go", "internal/http2/transport.go", "internal/http2/frame.go"}, "Unexpected production adaptation")
        require(len(manifest["adaptedCandidateFiles"]) == 3 and
                {v["path"] for v in manifest["adaptedCandidateFiles"]} == adaptations,
                "Incomplete adapted source hash inventory")
        for item in manifest["originalFiles"]:
            if item["path"] not in adaptations:
                data = (controlled / item["path"]).read_bytes()
                require(len(data) == item["size"] and hashlib.sha256(data).hexdigest() == item["sha256"], "Original source modified: " + item["path"])
        for item in manifest["adaptedCandidateFiles"]:
            data = (controlled / item["path"]).read_bytes()
            require(len(data) == item["bytes"] and hashlib.sha256(data).hexdigest() == item["sha256"], "Adapted source hash mismatch: " + item["path"])
        originals = {v["path"] for v in manifest["originalFiles"] if v["path"].endswith("_test.go")}
        omitted = {"internal/testdata/cert_test.go", "internal/testdata/testdata_suite_test.go"}
        require(len(originals) == 14 and originals - selected == omitted, "Original wildcard test selection changed")
        require(len(originals & selected) == 12, "Expected 12 selected original test files")
        report(evidence, "package-selection-proof.json", {"module": req[0], "selectedTests": sorted(selected), "originalSelected": sorted(originals & selected), "originalOmitted": sorted(omitted), "omission": "Original testdata helper suite is excluded by Go wildcard; cert_test.go lacks io import. No PASS claimed."})
    elif operation == "named":
        verify_named(backend, evidence, "named.json")
    elif operation == "all":
        passed = verify_named(backend, evidence, "all.json")
        packages = objects(evidence / "packages.json")
        expected = {v["ImportPath"] for v in packages if v.get("TestGoFiles") or v.get("XTestGoFiles")}
        require(expected <= passed, "Original package tests did not complete: " + repr(sorted(expected - passed)))
        skips = {v["Package"] for v in events(evidence / "all.json") if v.get("Action") == "skip" and "Test" not in v}
        require(not skips & expected, "Test-bearing package skipped")
        report(evidence, "all-package-completion-proof.json", {"testPackagesPassed": sorted(expected), "noTestFilePackagesSkipped": sorted(skips)})
    elif operation == "original":
        with urllib.request.urlopen("https://proxy.golang.org/github.com/imroc/req/v3/@v/v3.57.0.zip", timeout=60) as response:
            data = response.read()
        require(hashlib.sha256(data).hexdigest() == ZIP_SHA, "Official archive SHA mismatch")
        (evidence / "original.zip").write_bytes(data)
        prefix = MODULE + "@v3.57.0/"
        manifest = json.loads((backend / "third_party/req/UPSTREAM-MANIFEST.json").read_text())
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
        def normalized(values, replacement, main_modfile):
            result = []
            for value in values:
                value = dict(value)
                if value.get("Main"):
                    require(Path(value["GoMod"]).resolve() == main_modfile, "Unexpected main modfile")
                    value.pop("GoMod")
                if value["Path"] == MODULE:
                    require(Path(value["Replace"]["Dir"]).resolve() == replacement, "Incorrect old/current replacement")
                    value.pop("Dir", None)
                    value.pop("GoMod", None)
                    value.pop("Replace")
                result.append(value)
            return result
        require(normalized(original, backend / "third_party/req", backend / "go.mod") ==
                normalized(old, evidence / "original", evidence / "old.mod"), "Old-red MVS changed")
        current = (backend / "go.mod").read_text()
        oldmod = (evidence / "old.mod").read_text()
        expected = current.replace("replace " + MODULE + " => ./third_party/req", "replace " + MODULE + " => " + str(evidence / "original"))
        require(oldmod == expected, "Old modfile changed more than replace path")
        require((backend / "go.sum").read_bytes() == (evidence / "old.sum").read_bytes(), "Old-red sums changed")
        report(evidence, "old-graph-proof.json", {"identicalModules": len(old), "onlyChange": "req Replace path"})
    elif operation == "old-red":
        expected = inventory(backend)
        values = events(evidence / "old-red.json")
        package, test = expected["privatePackage"], expected["sameOldWireWitness"]
        require((evidence / "old-red.exit").read_text().strip() == "1", "Old witness needs actual assertion failure exit1")
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


def coverage(backend, evidence):
    proofs = []
    blocks = {}
    pattern = re.compile(r"(.+):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)")
    for kind in ("named", "all"):
        raw = (evidence / (kind + ".raw.out")).read_text().splitlines()
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
        (evidence / (kind + ".mapped.out")).write_text("\n".join(mapped) + "\n")
    report(evidence, "coverage-path-only-proof.json", proofs)
    result = []
    for name in ("flow.go", "transport.go", "frame.go"):
        relative = "internal/http2/" + name
        old = (evidence / "original" / relative).read_text().splitlines()
        new = (backend / "third_party/req" / relative).read_text().splitlines()
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
    report(evidence, "official-adapted-fix-block-coverage.json", result)
    require(all(v["statements"] > 0 and 100 * v["coveredStatements"] >= 85 * v["statements"] for v in result), "Actual added fix-block coverage below85; Codecov85 also remains required")


if __name__ == "__main__":
    main()
