#!/usr/bin/env python3
"""Remote CI lock generation only; never supplies application test evidence."""
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import signal
import stat
import subprocess
import sys
import tarfile
import time
import traceback


def require(value, message):
    if not value:
        raise AssertionError(message)


def save(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")


def seal(data):
    return {"bytes": len(data), "sha256": hashlib.sha256(data).hexdigest()}


def git(root, *args):
    return subprocess.check_output(["git", *args], cwd=root)


def snapshot(root, destination, label, allow_modified=(), archive=False):
    require(git(root, "rev-parse", "--show-object-format").strip() == b"sha1", "Unexpected Git object format")
    tree = git(root, "ls-tree", "-rz", "--full-tree", "HEAD")
    (destination / (label + ".git-tree")).write_bytes(tree)
    (destination / (label + ".git-index")).write_bytes(git(root, "ls-files", "--stage", "-z"))
    status = git(root, "status", "--porcelain", "--untracked-files=all")
    (destination / (label + ".status")).write_bytes(status)
    for options in (("--others", "--exclude-standard"), ("--others", "--ignored", "--exclude-standard")):
        require(not git(root, "ls-files", "-z", *options), "Untracked or ignored generated input: " + label)
    result = []
    files = []
    for item in tree.split(b"\0"):
        if not item:
            continue
        metadata, raw_path = item.split(b"\t", 1)
        mode, kind, blob = metadata.decode().split()
        name = raw_path.decode()
        require(kind == "blob" and mode in ("100644", "100755"), "Unexpected tracked object: " + name)
        relative = Path(name)
        require(not relative.is_absolute() and ".." not in relative.parts, "Unexpected tracked path")
        path = root / relative
        attributes = path.lstat()
        require(stat.S_ISREG(attributes.st_mode), "Tracked file is not regular: " + name)
        require(bool(attributes.st_mode & 0o111) == (mode == "100755"), "Tracked mode differs: " + name)
        data = path.read_bytes()
        actual = hashlib.sha1(b"blob " + str(len(data)).encode() + b"\0" + data).hexdigest()
        require(actual == blob or name in allow_modified, "Tracked bytes differ outside allowed lock path: " + name)
        result.append({"path": name, "mode": mode, "expectedBlob": blob, "actualBlob": actual, **seal(data)})
        files.append((name, mode, data))
    require(result and any(item["path"].startswith("frontend/") for item in result) and
            any(item["path"].startswith("backend/") for item in result), "Incomplete repository snapshot")
    save(destination / (label + ".json"), result)
    if archive:
        with tarfile.open(destination / (label + ".tar"), "w") as handle:
            for name, mode, data in files:
                member = tarfile.TarInfo(name)
                member.size = len(data)
                member.mode = 0o755 if mode == "100755" else 0o644
                handle.addfile(member, io.BytesIO(data))
    return result


def capture(evidence, name, argv, cwd, timeout=480):
    record = {"argv": argv, "cwd": str(cwd), "started": False, "exit": None,
              "timeout": False, "streamsComplete": False, "startedAt": time.time()}
    process = None
    stdout, stderr = b"", b""
    try:
        process = subprocess.Popen(argv, cwd=cwd, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                   start_new_session=True)
        record.update(started=True, pid=process.pid)
        try:
            stdout, stderr = process.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            record["timeout"] = True
            os.killpg(process.pid, signal.SIGKILL)
            stdout, stderr = process.communicate(timeout=15)
        record.update(exit=process.returncode, streamsComplete=True)
    except Exception:
        record["captureError"] = traceback.format_exc()
        if process is not None and process.poll() is None:
            os.killpg(process.pid, signal.SIGKILL)
            stdout, stderr = process.communicate(timeout=15)
            record.update(exit=process.returncode, streamsComplete=True)
    (evidence / (name + ".stdout")).write_bytes(stdout)
    (evidence / (name + ".stderr")).write_bytes(stderr)
    record.update(stdout=seal(stdout), stderr=seal(stderr), finishedAt=time.time())
    save(evidence / (name + ".process.json"), record)
    return record


def main():
    require(os.environ.get("GITHUB_ACTIONS") == "true", "Executable work is restricted to remote CI")
    source, controller, evidence = map(lambda item: Path(item).resolve(), sys.argv[1:])
    runner_temp = Path(os.environ["RUNNER_TEMP"]).resolve()
    require(evidence.parent == runner_temp and evidence.name.startswith("vue-i18n-lock-"), "Evidence ownership mismatch")
    require(not evidence.is_relative_to(source) and not evidence.is_relative_to(controller), "Evidence overlaps a checkout")
    evidence.mkdir(exist_ok=False)
    store = runner_temp / ("vue-i18n-lock-store-" + os.environ["GITHUB_RUN_ID"] + "-" + os.environ["GITHUB_RUN_ATTEMPT"])
    result = {"scope": "Actual lock generation only; no install/test/build/security/coverage PASS claim",
              "sourceHead": os.environ["SOURCE_HEAD"], "sourceTree": os.environ["SOURCE_TREE"],
              "controllerHead": os.environ["GITHUB_SHA"], "runId": os.environ["GITHUB_RUN_ID"],
              "attempt": os.environ["GITHUB_RUN_ATTEMPT"], "records": [], "errors": [], "complete": False}
    source_before = controller_before = None
    store_owned = False
    try:
        require(not store.exists(), "Store already exists; ownership is not established")
        store_owned = True
        require(git(source, "rev-parse", "HEAD").decode().strip() == result["sourceHead"], "Wrong exact source head")
        require(git(source, "rev-parse", "HEAD^{tree}").decode().strip() == result["sourceTree"], "Wrong exact source tree")
        require(git(controller, "rev-parse", "HEAD").decode().strip() == result["controllerHead"], "Wrong controller head")
        result["controllerTree"] = git(controller, "rev-parse", "HEAD^{tree}").decode().strip()
        require(not git(source, "status", "--porcelain") and not git(controller, "status", "--porcelain"), "Initial checkout is dirty")
        source_before = snapshot(source, evidence, "source-before", archive=True)
        controller_before = snapshot(controller, evidence, "controller-before", archive=True)
        package = source / "frontend/package.json"
        lock = source / "frontend/pnpm-lock.yaml"
        package_data = json.loads(package.read_text(encoding="utf-8"))
        require(package_data["dependencies"]["vue-i18n"] == "11.4.14", "Vue parent changed")
        require(package_data["dependencies"]["@lobehub/icons"] == "^4.0.2", "Icons parent changed")
        require(package_data["pnpm"]["overrides"]["query-string@9.3.1>decode-uri-component"] == "0.5.1",
                "Wrong narrowly scoped decode-uri-component override")
        (evidence / "package.json").write_bytes(package.read_bytes())
        (evidence / "original-pnpm-lock.yaml").write_bytes(lock.read_bytes())
        for name, argv, expected in (("node-version", ["node", "--version"], "v22."),
                                     ("pnpm-version", ["pnpm", "--version"], "9.15.9")):
            record = capture(evidence, name, argv, source, timeout=30)
            result["records"].append({"name": name, **record})
            require(record["exit"] == 0 and record["streamsComplete"] and not record["timeout"] and
                    "captureError" not in record, "Version command failed: " + name)
            output = (evidence / (name + ".stdout")).read_text(encoding="utf-8").strip()
            require(output.startswith(expected) if name == "node-version" else output == expected,
                    "Unexpected tool version: " + output)
        record = capture(evidence, "lock-generation", ["pnpm", "install", "--lockfile-only", "--ignore-scripts",
                         "--no-frozen-lockfile", "--store-dir", str(store)], source / "frontend")
        result["records"].append({"name": "lock-generation", **record})
        (evidence / "generated-pnpm-lock.yaml").write_bytes(lock.read_bytes())
        (evidence / "lock.diff").write_bytes(git(source, "diff", "--binary", "HEAD", "--", "frontend/pnpm-lock.yaml"))
        require(record["exit"] == 0 and record["streamsComplete"] and not record["timeout"] and
                "captureError" not in record, "Actual lock command did not finish successfully")
        require(lock.read_bytes() != (evidence / "original-pnpm-lock.yaml").read_bytes(), "Expected lock update is absent")
    except Exception:
        result["errors"].append(traceback.format_exc())
    finally:
        try:
            source_after = snapshot(source, evidence, "source-after", allow_modified=("frontend/pnpm-lock.yaml",), archive=True)
            controller_after = snapshot(controller, evidence, "controller-after")
            if source_before is not None:
                require([item["path"] for item in source_before] == [item["path"] for item in source_after], "Source inventory moved")
                changed = [after["path"] for before, after in zip(source_before, source_after) if before != after]
                result["changedSourcePaths"] = changed
                require(changed == ["frontend/pnpm-lock.yaml"], "Changed source paths are not exactly the lock")
            require(controller_before is not None and controller_before == controller_after, "Controller inputs moved")
            for label in ("source", "controller"):
                require((evidence / (label + "-before.git-index")).read_bytes() ==
                        (evidence / (label + "-after.git-index")).read_bytes(), "Git index moved: " + label)
            require(git(source, "status", "--porcelain") == b" M frontend/pnpm-lock.yaml\n", "Unexpected final source status")
            require(not git(controller, "status", "--porcelain"), "Controller is dirty after execution")
            result["originalLock"] = seal((evidence / "original-pnpm-lock.yaml").read_bytes())
            result["generatedLock"] = seal((evidence / "generated-pnpm-lock.yaml").read_bytes())
            require(git(source, "rev-parse", "HEAD").decode().strip() == result["sourceHead"], "Source HEAD moved")
        except Exception:
            result["errors"].append(traceback.format_exc())
        try:
            if store_owned and store.exists():
                require(store.parent == runner_temp and store.name == "vue-i18n-lock-store-" + result["runId"] + "-" + result["attempt"], "Store cleanup ownership differs")
                shutil.rmtree(store)
            result["storeOwnershipConfirmed"] = store_owned
            result["ownedStoreRemoved"] = store_owned and not store.exists()
        except Exception:
            result["errors"].append(traceback.format_exc())
        result["complete"] = not result["errors"]
        save(evidence / "actual-result.json", result)
    return 1 if result["errors"] else 0


if __name__ == "__main__":
    sys.exit(main())
