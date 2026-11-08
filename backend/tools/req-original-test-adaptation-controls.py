#!/usr/bin/env python3
"""Remote-only controls for exact original-test registration/selection integrity.

Uses actual preceding go-list JSON and complete external controlled-module copies.
It never runs Go tests, fabricates test PASS events or changes application inputs.
"""
import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import traceback

MODULE = "github.com/imroc/req/v3"


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def save(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")


def objects(raw):
    text = raw.decode("utf-8")
    decoder = json.JSONDecoder()
    result = []
    while text.strip():
        text = text.lstrip()
        value, offset = decoder.raw_decode(text)
        result.append(value)
        text = text[offset:]
    return result


def capture(directory, argv, cwd):
    directory.mkdir()
    record = {"argv": argv, "cwd": str(cwd), "started": False,
              "actualExit": None, "watchdog": False, "streamsComplete": False}
    stdout, stderr = b"", b""
    process = None
    try:
        process = subprocess.Popen(argv, cwd=cwd, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        record.update({"started": True, "pid": process.pid})
        try:
            stdout, stderr = process.communicate(timeout=60)
        except subprocess.TimeoutExpired:
            record["watchdog"] = True
            process.kill()
            stdout, stderr = process.communicate(timeout=15)
        record.update({"actualExit": process.returncode, "streamsComplete": True})
    except Exception:
        record["captureError"] = traceback.format_exc()
        if process is not None and process.poll() is None:
            try:
                process.kill()
                stdout, stderr = process.communicate(timeout=15)
                record.update({"actualExit": process.returncode, "streamsComplete": True})
            except Exception:
                record["terminationError"] = traceback.format_exc()
    (directory / "stdout").write_bytes(stdout)
    (directory / "stderr").write_bytes(stderr)
    record.update({"stdoutBytes": len(stdout), "stderrBytes": len(stderr),
                   "stdoutSHA256": hashlib.sha256(stdout).hexdigest(),
                   "stderrSHA256": hashlib.sha256(stderr).hexdigest()})
    save(directory / "process.json", record)
    return record


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--backend", required=True)
    parser.add_argument("--evidence", required=True)
    parser.add_argument("--actual-inputs", required=True)
    args = parser.parse_args()
    require(os.environ.get("GITHUB_ACTIONS") == "true", "Controls execute only in remote CI")
    backend = Path(args.backend).resolve()
    evidence = Path(args.evidence).resolve()
    actual = Path(args.actual_inputs).resolve()
    require(not evidence.is_relative_to(backend.parent), "Control evidence must be external")
    evidence.mkdir(parents=True, exist_ok=False)
    result = {"scope": "Source/selection checker controls only; not Go test execution or coverage.",
              "records": [], "errors": []}
    try:
        raw_modules = (actual / "modules.json").read_bytes()
        raw_packages = (actual / "packages.json").read_bytes()
        (evidence / "actual-modules.json").write_bytes(raw_modules)
        (evidence / "actual-packages.json").write_bytes(raw_packages)
        result["actualInputSeals"] = {"modules": hashlib.sha256(raw_modules).hexdigest(),
                                      "packages": hashlib.sha256(raw_packages).hexdigest()}
        modules, packages = objects(raw_modules), objects(raw_packages)
        controlled = backend / "third_party/req"
        checker = backend / "tools/req-http2-security-evidence.py"
        result["checkerSHA256"] = hashlib.sha256(checker.read_bytes()).hexdigest()
        cases = {"clean": None,
                 "unregistered": "Original test adaptation registration differs",
                 "wrong-hash": "Adapted original test hash mismatch:",
                 "missing-selected-identity": "Adapted original test file not selected"}
        for name, diagnostic in cases.items():
            root = evidence / ("case-" + name)
            case_backend = root / "backend"
            destination = case_backend / "third_party/req"
            shutil.copytree(controlled, destination)
            case_modules, case_packages = copy.deepcopy(modules), copy.deepcopy(packages)
            for module in case_modules:
                if module["Path"] == MODULE:
                    module["Dir"], module["GoMod"] = str(destination), str(destination / "go.mod")
                    module["Replace"]["Dir"], module["Replace"]["GoMod"] = str(destination), str(destination / "go.mod")
            for package in case_packages:
                relative = Path(package["Dir"]).resolve().relative_to(controlled)
                package["Dir"] = str(destination / relative)
            proof = evidence / ("proof-" + name)
            proof.mkdir()
            manifest_path = destination / "UPSTREAM-MANIFEST.json"
            manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
            if name == "unregistered":
                manifest["originalTestAdaptations"] = []
            elif name == "wrong-hash":
                manifest["originalTestAdaptations"][0]["candidateSHA256"] = "0" * 64
            elif name == "missing-selected-identity":
                matches = [package for package in case_packages if package["ImportPath"] == MODULE]
                require(len(matches) == 1 and "request_test.go" in matches[0]["TestGoFiles"], "Actual selected original test prerequisite missing")
                matches[0]["TestGoFiles"].remove("request_test.go")
            manifest_path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
            (proof / "modules.json").write_text("".join(json.dumps(value) + "\n" for value in case_modules), encoding="utf-8")
            (proof / "packages.json").write_text("".join(json.dumps(value) + "\n" for value in case_packages), encoding="utf-8")
            command_dir = evidence / ("command-" + name)
            command = capture(command_dir, [sys.executable, "-B", str(checker), "packages", str(case_backend), str(proof)], case_backend)
            require(command["started"] and command["streamsComplete"] and not command["watchdog"] and "captureError" not in command,
                    "Control failed to execute normally: " + name)
            stderr = (command_dir / "stderr").read_text(encoding="utf-8")
            if name == "clean":
                require(command["actualExit"] == 0, "Actual clean copied source failed: " + stderr)
                value = json.loads((proof / "package-selection-proof.json").read_text(encoding="utf-8"))
                require(len(value["originalSelected"]) == 12 and len(value["originalOmitted"]) == 2 and
                        value["originalTestAdaptations"] == manifest["originalTestAdaptations"], "Clean original identity proof incomplete")
            else:
                require(command["actualExit"] != 0 and diagnostic in stderr, "Corrupt original test registration escaped intended gate: " + name)
                require(not (proof / "package-selection-proof.json").exists(), "Rejected control published complete selection proof")
            result["records"].append({"case": name, "actual": command, "diagnostic": stderr, "passed": True})
        result["actualCases"] = len(result["records"])
    except Exception:
        result["errors"].append(traceback.format_exc())
    save(evidence / "results.json", result)
    return 1 if result["errors"] else 0


if __name__ == "__main__":
    sys.exit(main())
