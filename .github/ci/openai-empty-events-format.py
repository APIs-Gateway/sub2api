"""CI-only #1750 formatter evidence; never execute against a local checkout.

Copies are formatted outside all three frozen checkouts. Application branches
remain RAW until actual outputs have been reviewed and imported in normal Git
history. This program neither runs tests nor declares an application PASS.
"""

import argparse
import copy
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import stat
import subprocess
import tarfile
import time


PINS = {
    "old": ("5052c03eb672b7cd162bf3fa41042aa35d34cc25", "84ccc9c8b4ccbce9b35a642730089cb0813300ea"),
    "new": ("a2cebe17c05e5c806bd4019f8085c35864345f09", "bcf2097c5193b1ffc24d8b540b9e35daf09f8244"),
}
TEST = "backend/internal/service/openai_empty_event_retry_test.go"
PRODUCTION = "backend/internal/service/openai_gateway_service.go"
TEST_SHA = "281d29b9d886947737f484f8605f20fda91661ef6c4c64080534c4be806f3a10"
PRODUCTION_SHA = {
    "old": "1be9337c2118ee6b52828e41b523213bdf5bf387033d079000654caab84c095a",
    "new": "05f064685b60361a46b4e3524ab20c264518b9fc025c9b16a43e3f20ac57bc8b",
}
CONTROLLER_PATHS = {
    ".github/ci/openai-empty-events-format.py",
    ".github/workflows/openai-empty-events-format.yml",
    ".github/ci/OPENAI-EMPTY-EVENTS-1750-PROOF-DESIGN.md",
}
OPERATIONS = ("raw-diff", "write-copy", "formatted-diff", "formatted-list")


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


def prepare(workspace, evidence, controller_head):
    require(evidence.is_dir() and not any(evidence.iterdir()), "evidence directory must start empty")
    event = Path(os.environ["GITHUB_EVENT_PATH"])
    shutil.copyfile(event, evidence / "event.json")
    save(evidence / "identity.json", {
        "controllerEventHead": controller_head,
        "runId": os.environ["GITHUB_RUN_ID"], "runAttempt": os.environ["GITHUB_RUN_ATTEMPT"],
        "pins": PINS, "phase": "RAW formatter only; behavior ready=false",
    })
    snapshots, archives = {}, {}
    for label in ("old", "new", "controller"):
        root = workspace / label
        head, tree = PINS[label] if label in PINS else (controller_head, "")
        snapshots[label] = snapshot(root, head, tree, evidence / (label + "-before.json"))
        archive = evidence / (label + "-full-input.tar")
        with archive.open("wb") as output:
            subprocess.run(["git", "-C", str(root), "archive", "--format=tar", "HEAD"], stdout=output, check=True)
        archives[label] = verify_archive(archive, snapshots[label])
    source = {label: {item["path"]: item for item in data["files"]} for label, data in snapshots.items()}
    require(len(source["old"]) == 4307 and len(source["new"]) == 4307, "application input count changed")
    require(set(source["old"]) == set(source["new"]), "OLD/NEW input paths differ")
    changed = {name for name in source["old"] if source["old"][name] != source["new"][name]}
    require(changed == {PRODUCTION, "backend/internal/service/OPENAI-EMPTY-EVENTS-1750.md"}, "OLD/NEW differ outside reviewed production/provenance")
    require(set(source["controller"]) - set(source["new"]) == CONTROLLER_PATHS, "unexpected controller additions")
    require(all(source["controller"].get(name) == item for name, item in source["new"].items()), "controller modified application input")
    require(len(source["controller"]) == 4310, "controller input count changed")
    for head, _ in PINS.values():
        subprocess.run(["git", "-C", str(workspace / "controller"), "merge-base", "--is-ancestor", head, controller_head], check=True)
    inputs = []
    for label in ("old", "new"):
        for name in (TEST, PRODUCTION):
            raw = (workspace / label / name).read_bytes()
            expected = TEST_SHA if name == TEST else PRODUCTION_SHA[label]
            require(sha(raw) == expected, "pinned source hash mismatch: " + label + "/" + name)
            for kind in ("raw", "formatted"):
                target = evidence / kind / label / name
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(raw)
            inputs.append({"label": label, "path": name, "sha256": expected, "bytes": len(raw),
                           "importEligible": name == TEST or label == "new"})
    require((evidence / "raw" / "old" / TEST).read_bytes() == (evidence / "raw" / "new" / TEST).read_bytes(), "OLD/NEW test source mismatch")
    save(evidence / "format-inputs.json", inputs)
    save(evidence / "complete-input-archives.json", archives)
    for path in CONTROLLER_PATHS:
        destination = evidence / "executed-controller" / path
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(workspace / "controller" / path, destination)


def capture(prefix, argv):
    started = time.time_ns()
    timed_out = False
    with Path(str(prefix) + ".stdout").open("wb") as stdout, Path(str(prefix) + ".stderr").open("wb") as stderr:
        try:
            result = subprocess.run(argv, stdin=subprocess.DEVNULL, stdout=stdout, stderr=stderr, timeout=30)
            status = result.returncode
        except subprocess.TimeoutExpired:
            status, timed_out = None, True
    record = {"argv": argv, "cwd": os.getcwd(), "startedUnixNs": started,
              "finishedUnixNs": time.time_ns(), "actualExit": status, "timedOut": timed_out}
    save(Path(str(prefix) + ".process.json"), record)
    Path(str(prefix) + ".exit").write_text(str(status) + "\n")
    return record


def format_copies(evidence):
    go = shutil.which("go")
    require(go is not None, "Go missing")
    version = capture(evidence / "go-version", [go, "version"])
    require(version["actualExit"] == 0 and not version["timedOut"], "Go version failed")
    require((evidence / "go-version.stdout").read_bytes() == b"go version go1.26.10 linux/amd64\n", "wrong actual Go toolchain")
    require(not (evidence / "go-version.stderr").read_bytes(), "Go version stderr")
    goroot = capture(evidence / "go-root", [go, "env", "GOROOT"])
    require(goroot["actualExit"] == 0 and not goroot["timedOut"], "GOROOT command failed")
    require(not (evidence / "go-root.stderr").read_bytes(), "GOROOT stderr")
    formatter = Path((evidence / "go-root.stdout").read_text().strip()) / "bin/gofmt"
    require(formatter.is_absolute() and formatter.is_file(), "formatter binary missing")
    save(evidence / "actual-tools.json", {
        "go": {"path": str(Path(go).resolve()), "sha256": sha(Path(go).read_bytes())},
        "gofmt": {"path": str(formatter.resolve()), "sha256": sha(formatter.read_bytes())},
        "GOTOOLCHAIN": os.environ.get("GOTOOLCHAIN"), "GOWORK": os.environ.get("GOWORK"),
    })
    require(os.environ.get("GOTOOLCHAIN") == "local" and os.environ.get("GOWORK") == "off", "toolchain environment mismatch")
    inputs = json.loads((evidence / "format-inputs.json").read_text())
    for item in inputs:
        label, name = item["label"], item["path"]
        result = evidence / "results" / label / Path(name).name
        result.mkdir(parents=True, exist_ok=False)
        raw = str(evidence / "raw" / label / name)
        formatted = str(evidence / "formatted" / label / name)
        commands = (("raw-diff", [str(formatter), "-d", raw]),
                    ("write-copy", [str(formatter), "-w", formatted]),
                    ("formatted-diff", [str(formatter), "-d", formatted]),
                    ("formatted-list", [str(formatter), "-l", formatted]))
        for operation, argv in commands:
            capture(result / operation, argv)


def after(workspace, evidence, controller_head):
    failures = []
    for label in ("old", "new", "controller"):
        head, tree = PINS[label] if label in PINS else (controller_head, "")
        try:
            snapshot(workspace / label, head, tree, evidence / (label + "-after.json"))
            status, error = 0, None
        except Exception as exception:
            status, error = 1, repr(exception)
            failures.append(label + ": " + error)
        save(evidence / (label + "-after-result.json"), {"snapshotValidationStatus": status, "error": error})
    require(not failures, "; ".join(failures))


def validate_operation(operation, record, stdout, stderr, changed):
    require(record["timedOut"] is False, "operation timed out")
    status = record["actualExit"]
    require(type(status) is int and status >= 0, "operation actual exit missing or signaled")
    require(not stderr, "operation stderr")
    if operation == "raw-diff":
        require(status == 0 or (status == 1 and bool(stdout)), "raw diff invalid exit")
        require(bool(stdout) == changed, "raw diff and formatted bytes disagree")
    else:
        require(status == 0 and not stdout, "post-format operation failed or left output")


def controls(evidence):
    # Pure checker controls, run only in CI. They are not application tests.
    valid = {"actualExit": 0, "timedOut": False}
    validate_operation("formatted-list", valid, b"", b"", False)
    cases = [
        ("missing-exit", "formatted-list", {"actualExit": None, "timedOut": False}, b"", b"", False),
        ("signaled", "formatted-list", {"actualExit": -9, "timedOut": False}, b"", b"", False),
        ("timeout", "formatted-list", {"actualExit": None, "timedOut": True}, b"", b"", False),
        ("residual-list", "formatted-list", valid, b"still.go\n", b"", False),
        ("formatter-stderr", "write-copy", valid, b"", b"error\n", False),
        ("hidden-diff", "raw-diff", valid, b"", b"", True),
        ("invented-diff", "raw-diff", valid, b"diff\n", b"", False),
        ("nonzero-post-diff", "formatted-diff", {"actualExit": 1, "timedOut": False}, b"", b"", False),
    ]
    results = []
    for name, operation, record, stdout, stderr, changed in cases:
        try:
            validate_operation(operation, copy.deepcopy(record), stdout, stderr, changed)
        except RuntimeError as error:
            results.append({"name": name, "rejected": True, "reason": str(error)})
        else:
            results.append({"name": name, "rejected": False})
    save(evidence / "formatter-negative-controls.json", {"validControlAccepted": True, "cases": results})
    require(len(results) == 8 and all(item["rejected"] for item in results), "negative control accepted invalid evidence")


def judge(evidence):
    errors, results = [], []
    try:
        archives = json.loads((evidence / "complete-input-archives.json").read_text())
        require(set(archives) == {"old", "new", "controller"}, "incomplete archive set")
        for label in archives:
            before = evidence / (label + "-before.json")
            require(json.loads((evidence / (label + "-after-result.json")).read_text())["snapshotValidationStatus"] == 0, "failed after snapshot: " + label)
            require(before.read_bytes() == (evidence / (label + "-after.json")).read_bytes(), "frozen input changed: " + label)
            actual = verify_archive(evidence / (label + "-full-input.tar"), json.loads(before.read_text()))
            require(actual == archives[label], "archive metadata mismatch")
        inputs = json.loads((evidence / "format-inputs.json").read_text())
        require({(item["label"], item["path"]) for item in inputs} == {(label, name) for label in PINS for name in (TEST, PRODUCTION)} and len(inputs) == 4, "wrong formatter input set")
        for item in inputs:
            label, name = item["label"], item["path"]
            raw = (evidence / "raw" / label / name).read_bytes()
            formatted = (evidence / "formatted" / label / name).read_bytes()
            expected = TEST_SHA if name == TEST else PRODUCTION_SHA[label]
            require(sha(raw) == expected == item["sha256"] and len(raw) == item["bytes"], "raw input changed")
            operations = {}
            for operation in OPERATIONS:
                prefix = evidence / "results" / label / Path(name).name / operation
                record = json.loads(Path(str(prefix) + ".process.json").read_text())
                stdout = Path(str(prefix) + ".stdout").read_bytes()
                stderr = Path(str(prefix) + ".stderr").read_bytes()
                require(Path(str(prefix) + ".exit").read_text().strip() == str(record["actualExit"]), "exit evidence disagreement")
                validate_operation(operation, record, stdout, stderr, raw != formatted)
                operations[operation] = {"actualExit": record["actualExit"], "stdoutBytes": len(stdout), "stderrBytes": len(stderr)}
            results.append({"label": label, "path": name, "rawSha256": sha(raw), "formattedSha256": sha(formatted),
                            "rawBytes": len(raw), "formattedBytes": len(formatted), "changed": raw != formatted,
                            "importEligible": item["importEligible"], "operations": operations})
        require((evidence / "formatted" / "old" / TEST).read_bytes() == (evidence / "formatted" / "new" / TEST).read_bytes(), "OLD/NEW formatted test differs")
        require((evidence / "raw" / "old" / TEST).read_bytes() == (evidence / "raw" / "new" / TEST).read_bytes(), "OLD/NEW raw test differs")
        controls(evidence)
    except Exception as error:
        errors.append(repr(error))
    save(evidence / "formatter-actual-receipt.json", {
        "pins": PINS, "controllerHead": os.environ["GITHUB_SHA"], "results": results,
        "operationCount": sum(len(item["operations"]) for item in results), "errors": errors,
        "scope": "external-copy formatting only; RAW heads unchanged; OLD production output is evidence only, never import; behavior ready=false",
    })
    require(not errors and len(results) == 4 and sum(len(item["operations"]) for item in results) == 16, "formatter actual evidence rejected: " + repr(errors))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("phase", choices=("prepare", "format", "after", "judge"))
    parser.add_argument("--workspace", required=True, type=Path)
    parser.add_argument("--evidence", required=True, type=Path)
    args = parser.parse_args()
    workspace, evidence = args.workspace.resolve(), args.evidence.resolve()
    require(os.environ.get("GITHUB_ACTIONS") == "true" and os.environ.get("CI") == "true", "remote GitHub CI only")
    require(workspace != evidence and workspace not in evidence.parents and evidence not in workspace.parents, "evidence must be outside all checkouts")
    head = os.environ["GITHUB_SHA"]
    if args.phase == "prepare":
        prepare(workspace, evidence, head)
    elif args.phase == "format":
        format_copies(evidence)
    elif args.phase == "after":
        after(workspace, evidence, head)
    else:
        judge(evidence)


if __name__ == "__main__":
    main()
