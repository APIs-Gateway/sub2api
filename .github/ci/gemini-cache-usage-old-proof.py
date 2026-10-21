#!/usr/bin/env python3
"""CI-only OLD observations. Genuine test failures remain workflow failures."""
import collections
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import stat
import subprocess
import sys
import tarfile
import traceback

WORKSPACE = Path(os.environ["GITHUB_WORKSPACE"]).resolve()
CONTROLLER = WORKSPACE / "controller"
OLD = WORKSPACE / "old"
EVIDENCE = Path(os.environ["GEMINI_CACHE_OLD_EVIDENCE"]).resolve()
OLD_HEAD = "a0e34408a3f50393d91778269bd8bdc11730c3cf"
OLD_TREE = "0c4f747a391eb8474e42647e5f190eed414951b5"
NEW_HEAD = "a170de70f84bba8914591b41a885382c6f3f843d"
NEW_TREE = "6858a180e12d089d9b40cd499c582c4e85a765f2"
INVENTORY = ".github/ci/gemini-cache-usage-focused-inventory.json"
HELPER = ".github/ci/gemini-cache-usage-old-proof.py"
WORKFLOW = ".github/workflows/gemini-cache-usage-old-proof.yml"
PRODUCTION = "backend/internal/service/gemini_messages_compat_service.go"
FIXTURES = (
    "backend/internal/service/gemini_cache_usage_test.go",
    "backend/internal/repository/gemini_cache_usage_integration_test.go",
)
NEW_REFERENCE = {
    "actualRun": 39605017356, "actualEvent": "pull_request", "actualHead": NEW_HEAD,
    "actualTree": NEW_TREE, "artifact": 12347643829,
    "artifactSHA256": "7970ca7dc4a91268264c88c8a52d0061c2f02a9ba6eb82405edeaf4185b45454",
    "independentActualReceiptSHA256": "19eecd8604348130523e80cac755f31ecb3435648cb66d4e5d3606ef427f4236",
    "actualGoVersion": "go1.26.6 linux/amd64", "unitLeavesRUNandPASS": 33,
    "integrationLeavesRUNandPASS": 12,
    "limit": "Previously independently reviewed NEW evidence reference only. This workflow never executes NEW. Formal new security toolchain, coverage and other required checks remain separate.",
}


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def blob(data):
    return hashlib.sha1(b"blob " + str(len(data)).encode() + b"\0" + data).hexdigest()


def dump(name, value):
    path = EVIDENCE / name
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, ensure_ascii=False) + "\n")


def load(name):
    return json.loads((EVIDENCE / name).read_bytes())


def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args])


def entries(root, ref):
    result = {}
    for row in git(root, "ls-tree", "-rz", "--full-tree", ref).split(b"\0"):
        if row:
            meta, name = row.split(b"\t", 1)
            mode, kind, oid = meta.decode().split()
            name = os.fsdecode(name)
            require(name not in result and kind == "blob", "duplicate/unsupported tracked input")
            result[name] = {"mode": mode, "type": kind, "blob": oid}
    return result


def snapshot(label, root, expected_head):
    result = {"head": git(root, "rev-parse", "HEAD").decode().strip(),
              "tree": git(root, "rev-parse", "HEAD^{tree}").decode().strip(), "files": []}
    expected = entries(root, "HEAD")
    index = {}
    for suffix, args in (("ls-tree", ("ls-tree", "-rz", "--full-tree", "HEAD")),
                         ("index", ("ls-files", "--stage", "-z")),
                         ("status", ("status", "--porcelain=v1", "--untracked-files=all", "-z")),
                         ("ignored", ("ls-files", "--others", "--ignored", "--exclude-standard", "-z"))):
        data = git(root, *args)
        (EVIDENCE / (label + "." + suffix)).write_bytes(data)
        result[suffix + "SHA256"] = sha(data)
        if suffix == "status":
            require(not data, label + " checkout has tracked/untracked changes")
        if suffix == "index":
            for row in data.split(b"\0"):
                if row:
                    meta, name = row.split(b"\t", 1)
                    mode, oid, stage = meta.decode().split()
                    name = os.fsdecode(name)
                    require(stage == "0" and name not in index, "unmerged/duplicate index")
                    index[name] = {"mode": mode, "type": "blob", "blob": oid}
    require(result["head"] == expected_head and index == expected, label + " wrong exact head/index")
    for name, entry in expected.items():
        path = root / name
        physical = path.lstat()
        if entry["mode"] == "120000":
            require(stat.S_ISLNK(physical.st_mode), "expected source symlink")
            data, mode = os.fsencode(os.readlink(path)), "120000"
        else:
            require(stat.S_ISREG(physical.st_mode), "expected regular source input")
            data = path.read_bytes()
            mode = "100755" if physical.st_mode & 0o111 else "100644"
        require(mode == entry["mode"] and blob(data) == entry["blob"], label + " raw mode/blob differs: " + name)
        result["files"].append({"path": name, **entry, "bytes": len(data), "sha256": sha(data)})
    dump(label + ".json", result)
    return result


def archive(label, ref):
    target = EVIDENCE / (label + "-full-input.tar")
    with target.open("wb") as stream:
        completed = subprocess.run(["git", "-C", str(CONTROLLER), "archive", "--format=tar", ref], stdout=stream, stderr=subprocess.PIPE)
    (EVIDENCE / (label + "-archive.stderr")).write_bytes(completed.stderr)
    require(completed.returncode == 0, label + " git archive failed")
    expected = entries(CONTROLLER, ref)
    found = {}
    with tarfile.open(target) as source:
        for member in source.getmembers():
            if member.isdir():
                continue
            if member.issym():
                data, mode = os.fsencode(member.linkname), "120000"
            else:
                require(member.isfile(), "unsupported archive entry")
                data = source.extractfile(member).read()
                mode = "100755" if member.mode & 0o111 else "100644"
            require(member.name not in found, "duplicate archive entry")
            found[member.name] = {"mode": mode, "type": "blob", "blob": blob(data)}
    require(found == expected, label + " archive raw inputs differ from exact Git tree")
    dump(label + "-archive.json", {"head": ref, "tree": git(CONTROLLER, "rev-parse", ref + "^{tree}").decode().strip(),
                                  "files": len(found), "bytes": target.stat().st_size, "sha256": sha(target.read_bytes()),
                                  "actualGitArchiveExit": completed.returncode})


def command(label, args, timeout):
    target = EVIDENCE / "commands" / label
    target.parent.mkdir(parents=True, exist_ok=True)
    record = {"args": args, "cwd": str(OLD / "backend"), "timeoutSeconds": timeout}
    with Path(str(target) + ".stdout").open("wb") as out, Path(str(target) + ".stderr").open("wb") as err:
        try:
            process = subprocess.Popen(args, cwd=OLD / "backend", stdout=out, stderr=err, start_new_session=True)
            record["exit"] = process.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            record.update({"exit": 124, "timeout": True})
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            record["terminatedProcessExit"] = process.wait()
        except Exception as error:
            record.update({"exit": 127, "launchError": str(error)})
    Path(str(target) + ".exit").write_text(str(record["exit"]) + "\n")
    for channel in ("stdout", "stderr"):
        record[channel + "SHA256"] = sha(Path(str(target) + "." + channel).read_bytes())
    dump("commands/" + label + ".json", record)
    return record


def prepare():
    require(EVIDENCE.is_dir() and not EVIDENCE.is_relative_to(WORKSPACE), "fresh external evidence required")
    require(not (EVIDENCE / "old-before.json").exists(), "evidence already initialized")
    controller = snapshot("controller-before", CONTROLLER, os.environ["GITHUB_SHA"])
    old = snapshot("old-before", OLD, OLD_HEAD)
    require(old["tree"] == OLD_TREE and git(CONTROLLER, "rev-parse", NEW_HEAD + "^{tree}").decode().strip() == NEW_TREE, "wrong frozen OLD/NEW tree")
    old_entries, new_entries = entries(CONTROLLER, OLD_HEAD), entries(CONTROLLER, NEW_HEAD)
    controller_entries = entries(CONTROLLER, "HEAD")
    require(controller_entries.keys() == (new_entries.keys() | {HELPER, WORKFLOW}), "controller path composition differs")
    require(all(controller_entries[name] == value for name, value in new_entries.items()), "controller modified NEW application")
    added = set(new_entries) - set(old_entries)
    require(added == {INVENTORY, ".github/ci/gemini-cache-usage-focused.py"}, "unexpected OLD/NEW additions")
    changed = {name for name in old_entries if old_entries[name] != new_entries[name]}
    require(changed == {PRODUCTION, ".github/workflows/backend-ci.yml"}, "unexpected OLD/NEW changes")
    require(not set(old_entries) - set(new_entries), "NEW dropped OLD inputs")
    for name in (*FIXTURES, "backend/go.mod", "backend/go.sum"):
        require(git(CONTROLLER, "show", OLD_HEAD + ":" + name) == git(CONTROLLER, "show", NEW_HEAD + ":" + name), "OLD/NEW fixture or module input differs: " + name)
    inventory_bytes = git(CONTROLLER, "show", NEW_HEAD + ":" + INVENTORY)
    (EVIDENCE / "inventory.json").write_bytes(inventory_bytes)
    inventory = json.loads(inventory_bytes)
    require(len(inventory["unit"]["leaves"]) == 33 and len(inventory["integration"]["leaves"]) == 12, "wrong frozen inventory")
    for label in ("unit", "integration"):
        require(len(set(inventory[label]["leaves"])) == len(inventory[label]["leaves"]), "duplicate frozen expected leaf")
    for name, expected in inventory["fixtureSHA256"].items():
        require(sha((OLD / name).read_bytes()) == expected, "OLD fixture differs from accepted NEW")
    for label, ref in (("controller", controller["head"]), ("old", OLD_HEAD), ("new-reference", NEW_HEAD)):
        archive(label, ref)
    for name in (HELPER, WORKFLOW):
        target = EVIDENCE / "executed" / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes((CONTROLLER / name).read_bytes())
    dump("source-composition.json", {"oldHead": OLD_HEAD, "oldTree": OLD_TREE, "newReferenceHead": NEW_HEAD, "newReferenceTree": NEW_TREE,
                                    "controllerHead": controller["head"], "controllerTree": controller["tree"],
                                    "sameFixturesAndModules": True, "oldNewAdditions": sorted(added), "oldNewChanged": sorted(changed)})
    dump("existing-NEW-actual-reference.json", NEW_REFERENCE)
    for label, args in (("go-version", ["go", "version"]),
                        ("go-env", ["go", "env", "-json", "GOVERSION", "GOROOT", "GOPATH", "GOTOOLCHAIN", "GOMOD", "GOWORK", "GOFLAGS", "GOOS", "GOARCH", "CGO_ENABLED", "GOCACHE", "GOMODCACHE"])):
        require(command(label, args, 60)["exit"] == 0, label + " failed")
    require((EVIDENCE / "commands/go-version.stdout").read_text() == "go version go1.26.6 linux/amd64\n", "OLD/NEW historical Go versions differ")
    dump("execution-environment.json", {key: os.environ.get(key) for key in ("GOTOOLCHAIN", "GOWORK", "GOFLAGS", "GOMAXPROCS", "CGO_ENABLED", "GITHUB_SHA", "GITHUB_WORKFLOW_SHA", "GITHUB_WORKFLOW_REF", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT")})


def execute():
    require(load("prepare-result.json")["status"] == "PASS", "prepare failed")
    commands = (
        ("unit", ["go", "test", "-tags=unit", "-race", "-p=1", "-parallel=1", "-count=1", "-timeout=10m", "-json", "-run", "^TestGeminiCacheUsage_(Extractor|ForwardBoundaries|BlockedNativeRetainsSignalAndWire)$", "./internal/service/"], 1200),
        ("integration", ["go", "test", "-tags=integration", "-race", "-p=1", "-parallel=1", "-count=1", "-timeout=15m", "-json", "-run", "^TestGeminiCacheUsageHTTP_CacheOnlySettlesOnce$", "./internal/repository/"], 1500),
    )
    # Capture both actual commands even if either fails. No expected-RED waiver.
    results = [command(label, args, timeout) for label, args, timeout in commands]
    dump("both-commands-completed.json", results)
    require(all(result["exit"] == 0 for result in results), "actual OLD tests failed; real nonzero statuses are preserved")


def after():
    snapshot("controller-after", CONTROLLER, os.environ["GITHUB_SHA"])
    snapshot("old-after", OLD, OLD_HEAD)


def inspect_suite(label, expected):
    process = load("commands/" + label + ".json")
    require(process["exit"] == int((EVIDENCE / ("commands/" + label + ".exit")).read_text()), "exit metadata differs")
    require(process["exit"] in (0, 1) and not process.get("timeout") and not process.get("launchError"), "compile/watchdog/process failure requires separate review")
    stdout = (EVIDENCE / ("commands/" + label + ".stdout")).read_bytes()
    stderr = (EVIDENCE / ("commands/" + label + ".stderr")).read_bytes()
    require(sha(stdout) == process["stdoutSHA256"] and sha(stderr) == process["stderrSHA256"], "actual output digest differs")
    text = (stdout + stderr).decode()
    for marker in ("WARNING: DATA RACE", "panic:", "fatal error:", "test timed out after", "[build failed]", "[setup failed]", "signal: killed", "out of memory"):
        require(marker not in text, "incomplete runtime/compiler/race observation: " + marker)
    events = collections.defaultdict(list)
    outputs = collections.defaultdict(str)
    package_terminal = []
    rows = [json.loads(line) for line in stdout.splitlines() if line.strip()]
    allowed = set(expected["leaves"])
    for leaf in expected["leaves"]:
        parts = leaf.split("/")
        allowed.update("/".join(parts[:i]) for i in range(1, len(parts)))
    for row in rows:
        require(row.get("Package") == expected["package"], "unexpected package event")
        require(row["Action"] not in ("skip", "build-fail"), "skipped/build-failed observation")
        name = row.get("Test")
        if name:
            require(name in allowed, "unexpected test identity: " + name)
            events[name].append(row["Action"])
            outputs[name] += row.get("Output", "")
        elif row["Action"] in ("pass", "fail", "skip"):
            package_terminal.append(row["Action"])
    actual_leaves = {name for name in events if not any(other.startswith(name + "/") for other in events)}
    require(actual_leaves == set(expected["leaves"]), "missing or different actual leaf inventory")
    for name, actions in events.items():
        require(actions.count("run") == 1 and actions.count("pass") + actions.count("fail") == 1, "nonunique/incomplete case: " + name)
    failed = [name for name in expected["leaves"] if "fail" in events[name]]
    require(process["exit"] == (1 if failed else 0), "actual exit disagrees with test terminals")
    require(package_terminal == (["fail"] if failed else ["pass"]), "package terminal disagrees with cases")
    observations = []
    for name in expected["leaves"]:
        terminal = "fail" if "fail" in events[name] else "pass"
        classification = "PASS control" if terminal == "pass" else (
            "normalization assertion; independent source/precondition review still required" if "GEMINI_CACHE_USAGE_NORMALIZATION" in outputs[name]
            else "other test assertion; fixture/business classification requires independent source review")
        observations.append({"test": name, "actualRun": 1, "actualTerminal": terminal, "classification": classification,
                             "sourceAnchors": re.findall(r"[A-Za-z0-9_/.-]+\.go:\d+", outputs[name]), "actualOutput": outputs[name]})
    dump(label + "-actual-observations.json", {"actualExit": process["exit"], "FAIL": len(failed), "PASS": len(expected["leaves"]) - len(failed),
                                              "package": expected["package"], "leaves": observations})
    return {"actualExit": process["exit"], "FAIL": len(failed), "PASS": len(expected["leaves"]) - len(failed)}


def judge():
    for phase in ("prepare", "after"):
        require((EVIDENCE / (phase + ".exit")).read_text() == "0\n" and load(phase + "-result.json")["status"] == "PASS", phase + " incomplete")
    for label in ("controller", "old"):
        require(load(label + "-before.json") == load(label + "-after.json"), label + " full input bytes/modes changed")
        for suffix in ("ls-tree", "index", "status", "ignored"):
            require((EVIDENCE / (label + "-before." + suffix)).read_bytes() == (EVIDENCE / (label + "-after." + suffix)).read_bytes(), label + " Git state changed")
    inventory = load("inventory.json")
    results, errors = {}, []
    for label in ("unit", "integration"):
        try:
            results[label] = inspect_suite(label, inventory[label])
        except Exception as error:
            errors.append(label + ": " + str(error))
    dump("OLD-observation-review.json", {"results": results, "errors": errors, "claimLimit": "Actual test outcomes only; no failure whitelist and no source/PR approval."})
    require(not errors, "incomplete OLD evidence: " + repr(errors))
    failures = any(result["actualExit"] != 0 for result in results.values())
    require((EVIDENCE / "execute.exit").read_text() == ("1\n" if failures else "0\n"), "capture phase did not preserve actual test failure")
    require(load("execute-result.json")["status"] == ("FAIL" if failures else "PASS"), "capture phase status differs")
    dump("observation-complete.json", {"status": "OBSERVATION_COMPLETE", "OLD": results, "OLDTestsFailed": failures,
                                       "reusedNEW": NEW_REFERENCE, "workflowPolicy": "A real OLD test failure keeps the execute step and workflow failing. Observation completeness is not an application PASS, genuine-business classification, or PR approval."})


if __name__ == "__main__":
    phase = sys.argv[1]
    record = {"phase": phase, "status": "FAIL"}
    try:
        {"prepare": prepare, "execute": execute, "after": after, "judge": judge}[phase]()
        record["status"] = "PASS"
    except Exception as error:
        record["error"] = str(error)
        traceback.print_exc()
    finally:
        if EVIDENCE.is_dir():
            dump(phase + "-result.json", record)
    sys.exit(0 if record["status"] == "PASS" else 1)
