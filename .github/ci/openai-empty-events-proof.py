"""REMOTE CI ONLY. Locked draft: READY remains False until source and controller reviews.

98 new leaves are separate from the selected original159 tops/193subtests.
OLD's genuine nonzero test exit is never converted into a green job.
No runtime, compiler, syntax, checker or formatting validation occurred locally.
"""
import argparse
import collections
import copy
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import signal
import stat
import subprocess
import tarfile
import time

READY = False
PINS = {
    "old": ("16ff3b8d2e4c67dab07df5c44ee3441b0eda775a", "369e22b64885fb2f13ac977fc923eea83321feed"),
    "new": ("affe76ea781e3c9db4cf20867ee5fa6093620eb0", "f602e62f8f9c8eb58104eb72683025aaee3d49e4"),
}
TEST = "backend/internal/service/openai_empty_event_retry_test.go"
TEST_SHA = "b3c439eadee03165b218ab2490211574f2207a08af4375874535b4863c619a25"
REGRESSION_SHA = "601584014f937747786d07cb8aa76e562a42d54779b04a776693ece1eabcbaa7"
CONTROLLER_PATHS = {
    ".github/ci/openai-empty-events-proof.py",
    ".github/ci/openai-empty-events-existing-regressions.json",
    ".github/ci/openai-empty-events-new98.json",
    ".github/workflows/openai-empty-events-proof.yml",
}
NEW_TOPS = ("TestOpenAIEmptyEventPreOutputRetry", "TestOpenAIEmptyEventCommittedOutputDoesNotReplay", "TestOpenAIEmptyEventConservativeClassification")
ASSERTIONS = dict(zip(NEW_TOPS, ("OPENAI_EMPTY_PREOUTPUT_ASSERT", "OPENAI_EMPTY_REPLAY_GUARD_ASSERT", "OPENAI_EMPTY_CONSERVATIVE_ASSERT")))
BAD_OUTPUT = ("EMPTY_EVENT_FIXTURE", "WARNING: DATA RACE", "race detected during execution of test", "panic:", "fatal error:", "test timed out after", "[build failed]", "compile: signal: killed")
PEER_PATTERN = re.compile(r"EMPTY_EVENT_WIRE_COMPLETE (\S+) method=POST path=/backend-api/codex/responses bytes=([0-9]+) sha256=([0-9a-f]{64})(?:\n|$)")
COMPLETE_PATTERN = re.compile(r"OPENAI_EMPTY_PREOUTPUT_COMPLETE (\S+) requestScoped=true outputBytes=0(?:\n|$)")


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def save(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args])


def rows(raw):
    result = {}
    for row in raw.split(b"\0"):
        if not row:
            continue
        header, name = row.split(b"\t", 1)
        fields = header.decode().split()
        require(len(fields) == 3, "invalid Git index/tree row")
        mode, middle, last = fields
        blob = last if middle == "blob" else middle
        require(middle == "blob" or last == "0", "non-stage-zero entry")
        path = os.fsdecode(name)
        parts = PurePosixPath(path).parts
        require(parts and not path.startswith("/") and ".." not in parts, "unsafe Git path")
        require(path not in result and mode in ("100644", "100755", "120000"), "duplicate or unsupported Git entry")
        result[path] = (mode, blob)
    return result


def snapshot(root, expected_head, expected_tree, prefix):
    head = git(root, "rev-parse", "HEAD").decode().strip()
    tree = git(root, "rev-parse", "HEAD^{tree}").decode().strip()
    require(head == expected_head and (not expected_tree or tree == expected_tree), "wrong frozen Git head/tree")
    require(git(root, "rev-parse", "--show-object-format").strip() == b"sha1", "unsupported Git object format")
    status = git(root, "status", "--porcelain=v1", "--untracked-files=all")
    ignored = git(root, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
    index = git(root, "ls-files", "--stage", "-z")
    full_tree = git(root, "ls-tree", "-r", "-z", "HEAD")
    Path(str(prefix) + ".status").write_bytes(status)
    Path(str(prefix) + ".ignored").write_bytes(ignored)
    Path(str(prefix) + ".index").write_bytes(index)
    Path(str(prefix) + ".tree").write_bytes(full_tree)
    require(not status and not ignored, "untracked, ignored, or changed checkout input")
    indexed = rows(index)
    require(indexed == rows(full_tree), "stage-zero index differs from frozen tree")
    files = []
    for name, (mode, blob) in sorted(indexed.items()):
        target = root / name
        info = target.lstat()
        if mode == "120000":
            require(stat.S_ISLNK(info.st_mode), "expected symlink: " + name)
            raw = os.fsencode(os.readlink(target))
        else:
            require(stat.S_ISREG(info.st_mode) and not target.is_symlink(), "expected regular file: " + name)
            require(bool(info.st_mode & 0o111) == (mode == "100755"), "mode changed: " + name)
            raw = target.read_bytes()
        actual = hashlib.sha1(b"blob " + str(len(raw)).encode() + b"\0" + raw).hexdigest()
        require(actual == blob, "actual checkout bytes differ from Git blob: " + name)
        files.append({"path": name, "mode": mode, "blob": blob, "sha256": sha(raw), "bytes": len(raw)})
    result = {"head": head, "tree": tree, "files": files}
    save(prefix, result)
    return result


def verify_archive(archive, snapshot_data):
    expected = {item["path"]: item for item in snapshot_data["files"]}
    with tarfile.open(archive, "r:") as package:
        members = [member for member in package.getmembers() if not member.isdir()]
        require(len(members) == len(expected), "archive duplicate or missing file")
        require({member.name for member in members} == set(expected), "archive omitted or added tracked path")
        for member in members:
            item = expected[member.name]
            if item["mode"] == "120000":
                require(member.issym(), "archive changed symlink type")
                raw = os.fsencode(member.linkname)
            else:
                require(member.isfile(), "archive changed regular file type")
                require(bool(member.mode & 0o111) == (item["mode"] == "100755"), "archive changed executable mode")
                raw = package.extractfile(member).read()
            require(len(raw) == item["bytes"] and sha(raw) == item["sha256"], "archive changed input bytes")
    return {"bytes": archive.stat().st_size, "sha256": sha(archive.read_bytes()), "trackedFiles": len(expected)}


def inventory(controller):
    raw = (controller / ".github/ci/openai-empty-events-existing-regressions.json").read_bytes()
    require(sha(raw) == REGRESSION_SHA, "original regression inventory changed")
    original = json.loads(raw)
    new = json.loads((controller / ".github/ci/openai-empty-events-new98.json").read_text())
    leaves = new["expectedNewLeaves"]
    require(len(leaves) == len(set(leaves)) == 98, "new leaf inventory incomplete")
    require(original["topCount"] == 159 and original["expectedSubTests"] == 193 and original["expectedEventCount"] == 352, "selected original inventory changed")
    return original, leaves


def prepare(workspace, evidence, head):
    require(evidence.is_dir() and not any(evidence.iterdir()), "evidence directory must begin empty")
    shutil.copyfile(os.environ["GITHUB_EVENT_PATH"], evidence / "event.json")
    save(evidence / "identity.json", {"controllerHead": head, "pins": PINS, "ready": READY,
         "label": os.environ["PROOF_LABEL"], "run": os.environ["GITHUB_RUN_ID"], "attempt": os.environ["GITHUB_RUN_ATTEMPT"]})
    sources, archives = {}, {}
    for label in ("old", "new", "controller"):
        expected_head, expected_tree = PINS[label] if label in PINS else (head, "")
        data = snapshot(workspace / label, expected_head, expected_tree, evidence / (label + "-before.json"))
        sources[label] = {item["path"]: item for item in data["files"]}
        archive = evidence / (label + "-full-input.tar")
        with archive.open("wb") as output:
            subprocess.run(["git", "-C", str(workspace / label), "archive", "--format=tar", "HEAD"], stdout=output, check=True)
        archives[label] = verify_archive(archive, data)
    require(len(sources["old"]) == len(sources["new"]) == 4307 and len(sources["controller"]) == 4311, "wrong complete source counts")
    require(set(sources["controller"]) - set(sources["new"]) == CONTROLLER_PATHS, "unexpected controller additions")
    require(all(sources["controller"].get(path) == item for path, item in sources["new"].items()), "controller changed application bytes")
    original, leaves = inventory(workspace / "controller")
    for label in PINS:
        require(sha((workspace / label / TEST).read_bytes()) == TEST_SHA, "wrong formatted test input")
        for item in original["sources"] + original["auxiliarySources"]:
            raw = (workspace / label / item["path"]).read_bytes()
            require(len(raw) == item["bytes"] and sha(raw) == item["sha256"], "original regression/module/TestMain changed: " + item["path"])
        subprocess.run(["git", "-C", str(workspace / "controller"), "merge-base", "--is-ancestor", PINS[label][0], head], check=True)
    require((workspace / "old" / TEST).read_bytes() == (workspace / "new" / TEST).read_bytes(), "OLD/NEW fixture differs")
    save(evidence / "complete-input-archives.json", archives)
    save(evidence / "expected-selection.json", {"newLeaves": leaves, "originalTests": original["tests"], "regex": original["regex"]})
    for path in CONTROLLER_PATHS:
        target = evidence / "executed-controller" / path
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(workspace / "controller" / path, target)


def capture(prefix, argv, cwd, timeout_seconds):
    started = time.time_ns()
    timed_out = False
    with Path(str(prefix) + ".stdout").open("wb") as stdout, Path(str(prefix) + ".stderr").open("wb") as stderr:
        process = subprocess.Popen(argv, cwd=cwd, stdin=subprocess.DEVNULL, stdout=stdout, stderr=stderr, start_new_session=True)
        try:
            status = process.wait(timeout=timeout_seconds)
        except subprocess.TimeoutExpired:
            timed_out = True
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                status = process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                status = process.wait()
    result = {"argv": argv, "cwd": str(cwd), "startedUnixNs": started, "finishedUnixNs": time.time_ns(),
              "actualExit": status, "timedOut": timed_out, "streamsClosed": True}
    save(Path(str(prefix) + ".process.json"), result)
    Path(str(prefix) + ".exit").write_text(str(status) + "\n")
    return result


def json_stream(raw):
    text = raw.decode()
    decoder, result = json.JSONDecoder(), []
    while text.strip():
        text = text.lstrip()
        value, end = decoder.raw_decode(text)
        result.append(value)
        text = text[end:]
    return result


def graph_for_comparison(value, source, module_cache):
    # Keep every field and list order. Only the two actual checkout/cache roots
    # in explicit Go module filesystem fields are normalized across OLD/NEW
    # isolated jobs; original raw graph bytes remain the primary evidence.
    if isinstance(value, list):
        return [graph_for_comparison(item, source, module_cache) for item in value]
    if isinstance(value, dict):
        result = {}
        for key, item in value.items():
            if key in ("Dir", "GoMod") and isinstance(item, str):
                for root, marker in ((source, "<APPLICATION>"), (module_cache, "<MODULE_CACHE>")):
                    if item == root or item.startswith(root + "/"):
                        item = marker + item[len(root):]
                        break
                result[key] = item
            else:
                result[key] = graph_for_comparison(item, source, module_cache)
        return result
    return value


def execute(workspace, evidence):
    require(READY is True, "LOCKED: formatted application and controller require explicit reviewed readiness commit; elapsed time never authorizes execution")
    label = os.environ["PROOF_LABEL"]
    backend = workspace / label / "backend"
    original, _ = inventory(workspace / "controller")
    go = shutil.which("go")
    require(go is not None, "Go missing")
    version = capture(evidence / "go-version", [go, "version"], backend, 30)
    require(version["actualExit"] == 0 and not version["timedOut"] and
            (evidence / "go-version.stdout").read_bytes() == b"go version go1.26.10 linux/amd64\n" and
            not (evidence / "go-version.stderr").read_bytes(), "wrong actual Go1.26.10 toolchain")
    save(evidence / "go-binary.json", {"path": go, "sha256": sha(Path(go).read_bytes())})
    for name, argv, timeout_seconds in (
        ("go-env", [go, "env", "-json", "GOMOD", "GOWORK", "GOROOT", "GOCACHE", "GOMODCACHE", "GOOS", "GOARCH", "GOVERSION"], 30),
        ("deps-before", [go, "list", "-mod=readonly", "-tags=unit", "-race", "-p=1", "-deps", "-test", "-json", "./internal/service", "./internal/handler"], 600),
        ("modules-before", [go, "list", "-mod=readonly", "-m", "-json", "all"], 120),
    ):
        record = capture(evidence / name, argv, backend, timeout_seconds)
        require(record["actualExit"] == 0 and not record["timedOut"], "actual prerequisite failed: " + name)
    actual_env = json.loads((evidence / "go-env.stdout").read_text())
    require(actual_env["GOVERSION"] == "go1.26.10" and actual_env["GOOS"] == "linux" and actual_env["GOARCH"] == "amd64", "wrong test tool/platform")
    require(actual_env["GOMOD"] == str(backend / "go.mod") and actual_env["GOWORK"] == "off", "wrong module/workspace")
    for name in ("GOCACHE", "GOMODCACHE"):
        cache = Path(actual_env[name]).resolve()
        require(cache != workspace and workspace not in cache.parents, "cache inside frozen workspace")
    commands = [
        ("new98", "./internal/service", "^(" + "|".join(NEW_TOPS) + ")$"),
        ("original-service", "./internal/service", original["regex"]["./internal/service"]),
        ("original-handler", "./internal/handler", original["regex"]["./internal/handler"]),
    ]
    outcomes = []
    for name, package, pattern in commands:
        argv = [go, "test", "-tags=unit", "-mod=readonly", "-p=1", "-parallel=1", "-race", "-count=1", "-timeout=10m", "-json", "-run", pattern, package]
        # Capture all three actual commands even when OLD new98 fails. No shell
        # set-e, continue-on-error or inferred PASS can swallow original phases.
        outcomes.append({"phase": name, **capture(evidence / name, argv, backend, 900)})
        save(evidence / "test-process-outcomes.json", outcomes)
    modules_after = capture(evidence / "modules-after", [go, "list", "-mod=readonly", "-m", "-json", "all"], backend, 120)
    save(evidence / "go-binary-after.json", {"path": go, "sha256": sha(Path(go).read_bytes())})
    require(modules_after["actualExit"] == 0 and not modules_after["timedOut"], "module graph after failed")
    # This is the real command result, regardless of the later evidence audit.
    # In particular, complete genuine OLD business RED remains a failed job.
    require(all(item["actualExit"] == 0 and not item["timedOut"] for item in outcomes), "actual test command failed; preserve job FAIL and raw evidence")


def after(workspace, evidence, head):
    outcomes = []
    for label in ("old", "new", "controller"):
        expected_head, expected_tree = PINS[label] if label in PINS else (head, "")
        try:
            snapshot(workspace / label, expected_head, expected_tree, evidence / (label + "-after.json"))
            outcomes.append({"label": label, "snapshotValidationStatus": 0})
        except Exception as error:
            outcomes.append({"label": label, "snapshotValidationStatus": 1, "error": repr(error)})
    save(evidence / "after-snapshot-results.json", outcomes)
    require(all(item["snapshotValidationStatus"] == 0 for item in outcomes), "source after snapshot failed")


def classify(events, stderr, process, expected, package, new_leaves=None, old=False):
    require(process["timedOut"] is False and process["streamsClosed"] is True, "incomplete or timed-out process")
    require(type(process["actualExit"]) is int and process["actualExit"] >= 0, "missing or signaled actual exit")
    require(not stderr, "test process stderr is not acceptable")
    expected = set(expected)
    runs, terminals, output = collections.Counter(), {}, collections.defaultdict(str)
    package_terminal = []
    for event in events:
        require(type(event) is dict, "invalid event record")
        test, action = event.get("Test"), event.get("Action")
        text = event.get("Output", "")
        require(not any(marker in text for marker in BAD_OUTPUT), "fixture/build/race/panic/watchdog output")
        if test:
            require(event.get("Package") == package and test in expected, "unexpected test identity or package")
            if action == "run":
                runs[test] += 1
            elif action in ("pass", "fail", "skip"):
                require(test not in terminals and runs[test] == 1 and action != "skip", "duplicate, unrun or skipped terminal")
                terminals[test] = action
            elif action == "output":
                require(runs[test] == 1 and test not in terminals, "output outside actual running test")
                output[test] += text
            else:
                require(action in ("pause", "cont"), "unexpected test action")
        else:
            require("EMPTY_EVENT_WIRE_COMPLETE" not in text and "OPENAI_EMPTY_PREOUTPUT_COMPLETE" not in text and
                    not any(marker in text for marker in ASSERTIONS.values()), "fake package-level marker")
            if event.get("Package") == package and action in ("pass", "fail", "skip"):
                package_terminal.append(action)
    require(set(runs) == set(terminals) == expected and all(value == 1 for value in runs.values()), "missing or duplicate selected RUN/terminal identity")
    require(len(package_terminal) == 1, "missing or duplicate package terminal")
    failed = {name for name, action in terminals.items() if action == "fail"}
    if new_leaves is None:
        require(not failed and process["actualExit"] == 0 and package_terminal == ["pass"], "original regression did not fully PASS")
        return {"selectedIdentities": len(expected), "passed": len(expected), "scope": "selected original tests only"}
    leaves = set(new_leaves)
    require(leaves <= expected and len(leaves) == 98, "incorrect new inventory")
    wire_names = {name for name in leaves if not name.startswith(NEW_TOPS[2] + "/")}
    positive = {name for name in leaves if name.startswith(NEW_TOPS[0] + "/")}
    require(len(wire_names) == 69 and len(positive) == 45, "wrong wire/positive inventory")
    for name in expected:
        peers = PEER_PATTERN.findall(output[name])
        complete = COMPLETE_PATTERN.findall(output[name])
        require(len(peers) == (1 if name in wire_names else 0), "missing, duplicate or misplaced actual peer prerequisite")
        for actual_name, size, _ in peers:
            require(actual_name == name and int(size) > 0, "peer prerequisite belongs to another test")
        require(len(complete) == (1 if name in positive and terminals[name] == "pass" else 0), "positive completion marker disagrees with actual assertions")
        require(all(actual_name == name for actual_name in complete), "completion belongs to another test")
        if name in leaves:
            marker = ASSERTIONS[name.split("/", 1)[0]]
            if terminals[name] == "fail":
                require(output[name].count(marker + ":") == 1, "failed leaf lacks its actual business assertion")
                require(not any(other + ":" in output[name] for other in ASSERTIONS.values() if other != marker), "failed leaf contains another business assertion")
                require(not any(text in output[name] for text in ("context deadline exceeded", "context canceled", "Client.Timeout exceeded")), "new leaf failure caused by fixture context or client timeout")
            else:
                require(not any(value + ":" in output[name] for value in ASSERTIONS.values()), "passing leaf contains failed assertion")
    failed_leaves = failed & leaves
    # Parent failure propagation must exactly follow actual failed descendants.
    for name in expected - leaves:
        descendants_failed = any(leaf.startswith(name + "/") for leaf in failed_leaves)
        require((terminals[name] == "fail") == descendants_failed, "parent failure has no matching actual leaf or vice versa")
    if old:
        require(process["actualExit"] != 0 and package_terminal == ["fail"] and bool(failed_leaves & positive), "OLD has no genuine pre-output business RED")
    else:
        require(not failed and process["actualExit"] == 0 and package_terminal == ["pass"], "NEW did not fully PASS")
    return {"selectedIdentities": len(expected), "actualLeaves": 98, "peerPrerequisites": 69,
            "passedLeaves": len(leaves - failed_leaves), "failedLeaves": sorted(failed_leaves),
            "passedPositiveCompletions": len(positive - failed_leaves), "actualExit": process["actualExit"],
            "disposition": "genuine OLD business RED; job remains FAIL" if old else "NEW focused98 PASS"}


def controls(evidence, events, stderr, process, expected, package, leaves):
    # Mutate copies of genuine complete NEW events only. These are evidence
    # parser controls, never substitute JSON for application execution.
    classify(events, stderr, process, expected, package, leaves, False)
    first = leaves[0]
    variants = []
    variants.append(("missing-leaf", [event for event in events if event.get("Test") != first], process, stderr))
    run = next(event for event in events if event.get("Test") == first and event.get("Action") == "run")
    variants.append(("duplicate-run", events + [copy.deepcopy(run)], process, stderr))
    no_peer = copy.deepcopy(events)
    for event in no_peer:
        if event.get("Test") == first and "Output" in event:
            event["Output"] = PEER_PATTERN.sub("", event["Output"])
    variants.append(("missing-peer", no_peer, process, stderr))
    fake = copy.deepcopy(no_peer)
    fake.append({"Action": "output", "Package": package, "Output": "EMPTY_EVENT_WIRE_COMPLETE " + first + " method=POST path=/backend-api/codex/responses bytes=1 sha256=" + "0" * 64 + "\n"})
    variants.append(("fake-package-peer", fake, process, stderr))
    for name, text in (("compile-failure", "[build failed]"), ("fixture-failure", "EMPTY_EVENT_FIXTURE"), ("race-failure", "WARNING: DATA RACE"), ("watchdog", "panic: test timed out after")):
        changed = copy.deepcopy(events)
        changed.append({"Action": "output", "Package": package, "Output": text})
        variants.append((name, changed, process, stderr))
    variants.append(("missing-exit", events, {**process, "actualExit": None}, stderr))
    variants.append(("timeout-process", events, {**process, "timedOut": True}, stderr))
    variants.append(("stderr-process", events, process, b"uncaptured failure\n"))
    results = []
    for name, altered_events, altered_process, altered_stderr in variants:
        try:
            classify(altered_events, altered_stderr, altered_process, expected, package, leaves, False)
        except RuntimeError as error:
            results.append({"name": name, "rejected": True, "reason": str(error)})
        else:
            results.append({"name": name, "rejected": False})
    for name, fake_output in (("old-all-pass-with-nonzero-exit", ""), ("old-fake-business-marker-only", "OPENAI_EMPTY_PREOUTPUT_ASSERT: fabricated package marker\n")):
        changed = copy.deepcopy(events)
        if fake_output:
            changed.append({"Action": "output", "Package": package, "Output": fake_output})
        try:
            classify(changed, stderr, {**process, "actualExit": 1}, expected, package, leaves, True)
        except RuntimeError as error:
            results.append({"name": name, "rejected": True, "reason": str(error)})
        else:
            results.append({"name": name, "rejected": False})
    save(evidence / "classifier-negative-controls.json", {"actualNEWPositiveControlAccepted": True, "cases": results})
    require(len(results) == 13 and all(item["rejected"] for item in results), "classifier accepted corrupt evidence")


def judge(workspace, evidence):
    errors, results = [], {}
    try:
        require(READY is True, "locked draft cannot grant runtime acceptance")
        expected_execute_exit = "1" if os.environ["PROOF_LABEL"] == "old" else "0"
        require((evidence / "execute.exit").read_text().strip() == expected_execute_exit, "actual OLD failure or NEW success was not preserved by execute step")
        require((evidence / "after.exit").read_text().strip() == "0" and not (evidence / "after.stderr").read_bytes(), "after process failed")
        for label in ("old", "new", "controller"):
            before = evidence / (label + "-before.json")
            require(before.read_bytes() == (evidence / (label + "-after.json")).read_bytes(), "source bytes changed after execution")
            recorded = json.loads((evidence / "complete-input-archives.json").read_text())[label]
            require(verify_archive(evidence / (label + "-full-input.tar"), json.loads(before.read_text())) == recorded, "archive missing or changed")
        require(all(item["snapshotValidationStatus"] == 0 for item in json.loads((evidence / "after-snapshot-results.json").read_text())), "after snapshot failure")
        for name in ("go-version", "go-env", "deps-before", "modules-before", "modules-after"):
            process = json.loads((evidence / (name + ".process.json")).read_text())
            require(process["actualExit"] == 0 and process["timedOut"] is False and process["streamsClosed"] is True, "dependency/tool command failed")
            require((evidence / (name + ".exit")).read_text().strip() == "0", "dependency/tool raw exit mismatch")
        modules_before = json_stream((evidence / "modules-before.stdout").read_bytes())
        modules_after = json_stream((evidence / "modules-after.stdout").read_bytes())
        require(modules_before == modules_after and modules_before, "actual warm MVS metadata changed")
        backend = workspace / os.environ["PROOF_LABEL"] / "backend"
        actual_env = json.loads((evidence / "go-env.stdout").read_text())
        dependencies = json_stream((evidence / "deps-before.stdout").read_bytes())
        original, leaves = inventory(workspace / "controller")
        for package in ("service", "handler"):
            targets = [item for item in dependencies if item.get("ImportPath") == "github.com/Wei-Shaw/sub2api/internal/" + package]
            require(len(targets) == 1 and targets[0].get("Dir") == str(backend / "internal" / package), "actual dependency graph belongs to another package or checkout")
            test_files = set(targets[0].get("TestGoFiles", []))
            selected = {Path(item["sourcePath"]).name for item in original["tests"] if item["package"] == "./internal/" + package}
            if package == "service":
                selected.add(Path(TEST).name)
            require(selected <= test_files, "actual unit-tag package graph omitted selected test sources")
        main_modules = [item for item in modules_before if item.get("Main") is True]
        require(len(main_modules) == 1 and main_modules[0].get("Path") == "github.com/Wei-Shaw/sub2api" and main_modules[0].get("Dir") == str(backend) and main_modules[0].get("GoMod") == str(backend / "go.mod"), "actual MVS main module mismatch")
        application_root = str(workspace / os.environ["PROOF_LABEL"])
        save(evidence / "modules-comparison.json", {"normalization": {"onlyFields": ["Dir", "GoMod"], "applicationRoot": application_root, "moduleCacheRoot": actual_env["GOMODCACHE"]}, "modules": graph_for_comparison(modules_before, application_root, actual_env["GOMODCACHE"])})
        require((evidence / "go-binary.json").read_bytes() == (evidence / "go-binary-after.json").read_bytes(), "actual tool binary changed")
        new_expected = set(leaves) | set(NEW_TOPS) | {top + "/" + path for top in NEW_TOPS[:2] for path in ("native", "passthrough", "bridge")}
        require(len(new_expected) == 107, "new parent inventory changed")
        for name, suffix, expected in (
            ("new98", "service", new_expected),
            ("original-service", "service", {event for test in original["tests"] if test["package"] == "./internal/service" for event in test["expectedEvents"]}),
            ("original-handler", "handler", {event for test in original["tests"] if test["package"] == "./internal/handler" for event in test["expectedEvents"]}),
        ):
            process = json.loads((evidence / (name + ".process.json")).read_text())
            require((evidence / (name + ".exit")).read_text().strip() == str(process["actualExit"]), "test raw exit mismatch")
            events = [json.loads(line) for line in (evidence / (name + ".stdout")).read_bytes().splitlines() if line.strip()]
            stderr = (evidence / (name + ".stderr")).read_bytes()
            package = "github.com/Wei-Shaw/sub2api/internal/" + suffix
            results[name] = classify(events, stderr, process, expected, package,
                                     leaves if name == "new98" else None,
                                     os.environ["PROOF_LABEL"] == "old" and name == "new98")
            if name == "new98" and os.environ["PROOF_LABEL"] == "new":
                controls(evidence, events, stderr, process, expected, package, leaves)
    except Exception as error:
        errors.append(repr(error))
    save(evidence / "actual-scope-audit.json", {"head": PINS[os.environ["PROOF_LABEL"]][0], "label": os.environ["PROOF_LABEL"],
         "controllerHead": os.environ["GITHUB_SHA"], "ready": READY, "results": results, "errors": errors,
         "scope": "selected original159tops/193subnames plus new98 only; not whole-package suite/85/security approval; OLD actual failure stays job FAIL"})
    require(not errors and len(results) == 3, "actual scope evidence failed closed: " + repr(errors))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("phase", choices=("prepare", "execute", "after", "judge"))
    parser.add_argument("--workspace", required=True, type=Path)
    parser.add_argument("--evidence", required=True, type=Path)
    args = parser.parse_args()
    workspace, evidence = args.workspace.resolve(), args.evidence.resolve()
    require(os.environ.get("GITHUB_ACTIONS") == "true" and os.environ.get("CI") == "true", "remote CI only")
    require(os.environ["PROOF_LABEL"] in PINS, "unknown application phase")
    require(workspace != evidence and workspace not in evidence.parents and evidence not in workspace.parents, "evidence outside all source inputs required")
    if args.phase == "prepare":
        prepare(workspace, evidence, os.environ["GITHUB_SHA"])
    elif args.phase == "execute":
        execute(workspace, evidence)
    elif args.phase == "after":
        after(workspace, evidence, os.environ["GITHUB_SHA"])
    else:
        judge(workspace, evidence)


if __name__ == "__main__":
    main()


