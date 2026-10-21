#!/usr/bin/env python3
"""Run only in remote CI; retain actual focused Gemini test evidence."""
import collections
import hashlib
import json
import os
from pathlib import Path
import signal
import stat
import subprocess
import sys
import traceback

ROOT = Path(os.environ["GITHUB_WORKSPACE"]).resolve()
EVIDENCE = Path(os.environ["GEMINI_CACHE_USAGE_EVIDENCE"]).resolve()
INVENTORY = ".github/ci/gemini-cache-usage-focused-inventory.json"
WORKFLOW = ".github/workflows/backend-ci.yml"
HELPER = ".github/ci/gemini-cache-usage-focused.py"
SOURCES = (
    "backend/internal/service/gemini_cache_usage_test.go",
    "backend/internal/repository/gemini_cache_usage_integration_test.go",
    "backend/internal/service/gemini_messages_compat_service.go",
)


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def write_json(name, value):
    target = EVIDENCE / name
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(json.dumps(value, indent=2, ensure_ascii=False) + "\n")


def read_json(name):
    return json.loads((EVIDENCE / name).read_bytes())


def git(*args):
    return subprocess.check_output(["git", "-C", str(ROOT), *args])


def snapshot(phase):
    head = git("rev-parse", "HEAD").decode().strip()
    tree = git("rev-parse", "HEAD^{tree}").decode().strip()
    raw_tree = git("ls-tree", "-rz", "--full-tree", "HEAD")
    raw_index = git("ls-files", "--stage", "-z")
    raw_status = git("status", "--porcelain=v1", "--untracked-files=all", "-z")
    for name, data in (("ls-tree", raw_tree), ("index", raw_index), ("status", raw_status)):
        (EVIDENCE / (phase + "." + name)).write_bytes(data)
    expected = {}
    for row in raw_tree.split(b"\0"):
        if row:
            meta, name = row.split(b"\t", 1)
            mode, kind, blob = meta.decode().split()
            require(kind == "blob", "unsupported tracked object type")
            expected[os.fsdecode(name)] = (mode, blob)
    index = {}
    for row in raw_index.split(b"\0"):
        if row:
            meta, name = row.split(b"\t", 1)
            mode, blob, stage = meta.decode().split()
            require(stage == "0" and os.fsdecode(name) not in index, "unmerged or duplicate index")
            index[os.fsdecode(name)] = (mode, blob)
    result = {
        "head": head, "tree": tree, "trackedFiles": len(expected),
        "lsTreeSHA256": sha(raw_tree), "indexSHA256": sha(raw_index),
        "statusSHA256": sha(raw_status), "files": [],
    }
    write_json(phase + ".json", result)
    require(index == expected, "index differs from HEAD")
    require(not raw_status, "tracked or untracked checkout changes")
    require(head == os.environ["GITHUB_SHA"], "actual checkout differs from workflow SHA")
    for name in (*SOURCES, HELPER, INVENTORY, WORKFLOW):
        path = ROOT / name
        physical = path.lstat()
        require(stat.S_ISREG(physical.st_mode), "expected regular tracked source: " + name)
        data = path.read_bytes()
        mode = "100755" if physical.st_mode & 0o111 else "100644"
        blob = hashlib.sha1(b"blob " + str(len(data)).encode() + b"\0" + data).hexdigest()
        require(expected.get(name) == (mode, blob), "raw source mode/blob differs from HEAD: " + name)
        target = EVIDENCE / "sources" / phase / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
        result["files"].append({"path": name, "mode": mode, "blob": blob, "bytes": len(data), "sha256": sha(data)})
    write_json(phase + ".json", result)
    return result


def command(label, args, timeout):
    out = EVIDENCE / "commands" / label
    out.parent.mkdir(parents=True, exist_ok=True)
    record = {"args": args, "cwd": str(ROOT / "backend"), "timeoutSeconds": timeout}
    with Path(str(out) + ".stdout").open("wb") as stdout, Path(str(out) + ".stderr").open("wb") as stderr:
        try:
            process = subprocess.Popen(args, cwd=ROOT / "backend", stdout=stdout, stderr=stderr, start_new_session=True)
            record["exit"] = process.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            record.update({"exit": 124, "timeout": True})
            # Terminate only this command's isolated process group, including Go children.
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            record["terminatedProcessExit"] = process.wait()
        except Exception as error:
            record.update({"exit": 127, "launchError": str(error)})
    Path(str(out) + ".exit").write_text(str(record["exit"]) + "\n")
    for channel in ("stdout", "stderr"):
        record[channel + "SHA256"] = sha(Path(str(out) + "." + channel).read_bytes())
    write_json("commands/" + label + ".json", record)
    return record


def prepare():
    require(EVIDENCE.is_dir() and not (EVIDENCE / "before.json").exists(), "evidence directory must be freshly initialized")
    require(not EVIDENCE.is_relative_to(ROOT), "evidence must be outside the checkout")
    inventory = json.loads((ROOT / INVENTORY).read_bytes())
    require(inventory["schema"] == 1 and len(inventory["unit"]["leaves"]) == 33 and len(inventory["integration"]["leaves"]) == 12, "wrong focused inventory")
    for key in ("unit", "integration"):
        require(len(set(inventory[key]["leaves"])) == len(inventory[key]["leaves"]), "duplicate expected leaf")
    before = snapshot("before")
    (EVIDENCE / "inventory.json").write_bytes((ROOT / INVENTORY).read_bytes())
    event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_bytes())
    pull = event.get("pull_request") or {}
    write_json("execution-identity.json", {
        "actualHead": before["head"], "actualTree": before["tree"],
        "workflowSHA": os.environ["GITHUB_SHA"], "workflowRef": os.environ.get("GITHUB_WORKFLOW_REF"),
        "workflowSourceSHA": os.environ.get("GITHUB_WORKFLOW_SHA"),
        "event": os.environ["GITHUB_EVENT_NAME"], "runID": os.environ["GITHUB_RUN_ID"],
        "attempt": os.environ["GITHUB_RUN_ATTEMPT"], "pullRequestHead": (pull.get("head") or {}).get("sha"),
        "note": "PR checkout may be GitHub's merge commit; actual checkout and PR head are recorded separately.",
    })
    for label, args in (
        ("go-version", ["go", "version"]),
        ("go-env", ["go", "env", "-json", "GOVERSION", "GOROOT", "GOPATH", "GOTOOLCHAIN", "GOMOD", "GOWORK", "GOFLAGS", "GOOS", "GOARCH", "CGO_ENABLED", "GOCACHE", "GOMODCACHE"]),
    ):
        require(command(label, args, 60)["exit"] == 0, label + " failed")
    write_json("execution-environment.json", {name: os.environ.get(name) for name in ("GOMAXPROCS", "GOFLAGS", "GOTOOLCHAIN", "GOWORK", "CGO_ENABLED")})


def execute():
    require(read_json("prepare-result.json")["status"] == "PASS", "prepare incomplete")
    commands = (
        ("unit", ["go", "test", "-tags=unit", "-race", "-p=1", "-parallel=1", "-count=1", "-timeout=10m", "-json", "-run", "^TestGeminiCacheUsage_(Extractor|ForwardBoundaries|BlockedNativeRetainsSignalAndWire)$", "./internal/service/"], 1200),
        ("integration", ["go", "test", "-tags=integration", "-race", "-p=1", "-parallel=1", "-count=1", "-timeout=15m", "-json", "-run", "^TestGeminiCacheUsageHTTP_CacheOnlySettlesOnce$", "./internal/repository/"], 1500),
    )
    # Neither a failing unit command nor its checker can skip integration.
    results = [command(label, args, timeout) for label, args, timeout in commands]
    write_json("both-commands-completed.json", results)
    require(all(result["exit"] == 0 for result in results), "actual focused command failed; raw statuses preserved")


def after():
    # A separate always() step captures the checkout even when execute failed.
    snapshot("after")


def audit_suite(label, expected):
    process = read_json("commands/" + label + ".json")
    require(process["exit"] == int((EVIDENCE / ("commands/" + label + ".exit")).read_text()) == 0, label + " actual exit is not zero")
    require(not process.get("timeout") and not process.get("launchError"), label + " incomplete process")
    stdout = (EVIDENCE / ("commands/" + label + ".stdout")).read_bytes()
    stderr = (EVIDENCE / ("commands/" + label + ".stderr")).read_bytes()
    require(sha(stdout) == process["stdoutSHA256"] and sha(stderr) == process["stderrSHA256"], label + " captured bytes differ")
    text = (stdout + stderr).decode()
    for marker in ("WARNING: DATA RACE", "panic:", "fatal error:", "test timed out after", "[build failed]", "[setup failed]"):
        require(marker not in text, label + " runtime/compiler failure: " + marker)
    rows = [json.loads(line) for line in stdout.splitlines() if line.strip()]
    events = collections.defaultdict(list)
    package_terminal = []
    observed = {}
    allowed = set(expected["leaves"])
    for leaf in expected["leaves"]:
        parts = leaf.split("/")
        allowed.update("/".join(parts[:i]) for i in range(1, len(parts)))
    for row in rows:
        require(row.get("Package") == expected["package"], label + " unexpected package")
        action = row["Action"]
        require(action not in ("fail", "skip", "build-fail"), label + " failed or skipped event")
        if "Test" in row:
            require(row["Test"] in allowed, label + " unexpected test identity: " + row["Test"])
            events[row["Test"]].append(action)
        elif action in ("pass", "fail", "skip"):
            package_terminal.append(action)
    require(package_terminal == ["pass"], label + " package did not PASS once")
    for test, actions in events.items():
        require(actions.count("run") == 1 and actions.count("pass") == 1, label + " nonunique/incomplete test: " + test)
    for leaf in expected["leaves"]:
        require(leaf in events, label + " missing expected leaf: " + leaf)
        observed[leaf] = {"run": events[leaf].count("run"), "pass": events[leaf].count("pass")}
    # No expected leaf may be replaced by a narrower or differently named case.
    actual_leaves = {name for name in events if not any(other.startswith(name + "/") for other in events)}
    require(actual_leaves == set(expected["leaves"]), label + " actual leaf inventory differs")
    write_json(label + "-actual-leaves.json", {"package": expected["package"], "leaves": observed, "actualExit": process["exit"]})


def judge():
    errors = []
    for phase in ("prepare", "execute", "after"):
        try:
            require((EVIDENCE / (phase + ".exit")).read_text() == "0\n", phase + " failed or missing")
            require(read_json(phase + "-result.json")["status"] == "PASS", phase + " incomplete")
        except Exception as error:
            errors.append(str(error))
    require(read_json("before.json") == read_json("after.json"), "checkout identity/source bytes changed")
    for suffix in ("ls-tree", "index", "status"):
        require((EVIDENCE / ("before." + suffix)).read_bytes() == (EVIDENCE / ("after." + suffix)).read_bytes(), "checkout " + suffix + " changed")
    inventory = read_json("inventory.json")
    before_files = {f["path"]: f for f in read_json("before.json")["files"]}
    for source in inventory["fixtureSHA256"]:
        require(before_files[source]["sha256"] == inventory["fixtureSHA256"][source], "fixture bytes differ from frozen inventory: " + source)
    for label in ("unit", "integration"):
        try:
            audit_suite(label, inventory[label])
        except Exception as error:
            errors.append(str(error))
    write_json("focused-review.json", {"errors": errors, "expectedUnitLeaves": 33, "expectedIntegrationLeaves": 12, "actualHead": read_json("before.json")["head"], "actualTree": read_json("before.json")["tree"]})
    require(not errors, "focused actual evidence failed: " + repr(errors))
    write_json("complete.json", {"status": "PASS", "unitLeaves": 33, "integrationLeaves": 12, "allRequiredChecks": "NOT_REPLACED"})


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
            write_json(phase + "-result.json", record)
    sys.exit(0 if record["status"] == "PASS" else 1)
