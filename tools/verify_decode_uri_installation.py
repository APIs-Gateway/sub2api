#!/usr/bin/env python3
"""Remote CI only: real owner-chain decode contracts and official installed bytes.

No application exploit reachability claim; no advisory exception or gate waiver.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import signal
import stat
import subprocess
import sys
import tarfile
import traceback
import urllib.parse
import urllib.request

VERSION = "0.5.1"
TARBALL = "https://registry.npmjs.org/decode-uri-component/-/decode-uri-component-0.5.1.tgz"
SRI = "sha512-vWHQkXgg2cE3BB4rR0c+PYw9BCQzLoNIRMI7AunIkZst74tw/vpPp7VLoYtsR/cy1f55g3XDWUslfeCZjiZJyA=="
SHA1 = "7f66b25bf435df723f9af55713dcf860a1c297d2"
GROUPS = [
    "decode.valid-ascii-utf8", "decode.malformed-bom", "decode.single-pass",
    "query.plus-space-encoded-plus", "query.duplicates-null-empty-roundtrip",
    "query.decode-false-numbers-booleans", "query.malformed-tail",
    "decode.publisher-distinct-runs-32000",
]


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def save(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")


def capture(evidence, label, argv, cwd):
    record = {"argv": argv, "cwd": str(cwd), "started": False,
              "actualExit": None, "watchdog": False, "streamsComplete": False}
    stdout, stderr = b"", b""
    process = None
    try:
        process = subprocess.Popen(argv, cwd=cwd, stdout=subprocess.PIPE,
                                   stderr=subprocess.PIPE, start_new_session=True)
        record.update({"started": True, "pid": process.pid})
        try:
            stdout, stderr = process.communicate(timeout=180)
        except subprocess.TimeoutExpired as error:
            record["watchdog"] = True
            stdout, stderr = error.output or b"", error.stderr or b""
            try:
                process.kill() if os.name == "nt" else os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            stdout, stderr = process.communicate(timeout=30)
        record.update({"actualExit": process.returncode, "streamsComplete": True})
    except Exception:
        record["captureError"] = traceback.format_exc()
        if process is not None and process.poll() is None:
            try:
                process.kill() if os.name == "nt" else os.killpg(process.pid, signal.SIGKILL)
                stdout, stderr = process.communicate(timeout=30)
                record.update({"actualExit": process.returncode, "streamsComplete": True})
            except Exception:
                record["terminationError"] = traceback.format_exc()
    (evidence / (label + ".stdout")).write_bytes(stdout)
    (evidence / (label + ".stderr")).write_bytes(stderr)
    record.update({"stdoutBytes": len(stdout), "stdoutSHA256": sha(stdout),
                   "stderrBytes": len(stderr), "stderrSHA256": sha(stderr)})
    save(evidence / (label + ".process.json"), record)
    require(record["started"] and record["actualExit"] == 0 and not record["watchdog"]
            and record["streamsComplete"] and "captureError" not in record,
            "Actual observation command failed: " + label)
    return stdout


def snapshot(repo, expected_head, evidence, label):
    def git(*args):
        return subprocess.check_output(["git", "-C", str(repo), *args])
    head, tree = git("rev-parse", "HEAD", "HEAD^{tree}").decode().splitlines()
    require(head == expected_head, "Source differs from exact event head")
    expected = {}
    for row in git("ls-tree", "--full-tree", "-r", "-z", tree).split(b"\0"):
        if row:
            metadata, name = row.split(b"\t", 1)
            mode, kind, blob = metadata.decode().split()
            require(kind == "blob" and mode in ("100644", "100755", "120000") and name not in expected,
                    "Invalid/duplicate tracked source entry")
            expected[name] = (mode, blob)
    raw_index = git("ls-files", "--stage", "-z")
    (evidence / (label + ".index")).write_bytes(raw_index)
    index = {}
    for row in raw_index.split(b"\0"):
        if row:
            metadata, name = row.split(b"\t", 1)
            mode, blob, stage = metadata.decode().split()
            require(stage == "0" and name not in index, "Unmerged/duplicate source index")
            index[name] = (mode, blob)
    require(index == expected, "Source index differs from frozen HEAD tree")
    status = git("status", "--porcelain=v1", "--untracked-files=all")
    (evidence / (label + ".status")).write_bytes(status)
    require(not status, "Tracked/untracked checkout differs; installed node_modules remains Git-ignored")
    files = []
    for name, (mode, blob) in expected.items():
        path = repo / os.fsdecode(name)
        info = path.lstat()
        if mode == "120000":
            require(stat.S_ISLNK(info.st_mode), "Source symlink type differs")
            data = os.fsencode(os.readlink(path))
        else:
            require(stat.S_ISREG(info.st_mode) and bool(info.st_mode & 0o111) == (mode == "100755"),
                    "Source regular file/mode differs")
            data = path.read_bytes()
        require(hashlib.sha1(b"blob " + str(len(data)).encode() + b"\0" + data).hexdigest() == blob,
                "Source bytes differ: " + os.fsdecode(name))
        files.append({"path": os.fsdecode(name), "mode": mode, "blob": blob,
                      "bytes": len(data), "sha256": sha(data)})
    result = {"head": head, "tree": tree, "files": files}
    save(evidence / (label + ".json"), result)
    return result


def installed_evidence(repo, evidence, discovery):
    chain = ["@lobehub/icons", "@lobehub/ui", "query-string", "decode-uri-component"]
    versions = ["4.0.2", "4.9.2", "9.3.1", VERSION]
    edges = discovery["edges"]
    require([item["requested"] for item in edges] == chain, "Actual owner dependency chain differs")
    manifests = {}
    owner_copies = evidence / "owner-manifests"
    owner_copies.mkdir(exist_ok=False)
    manifest_seals = []
    for index, item in enumerate(discovery["manifests"]):
        root = Path(item["root"])
        require(root.is_absolute() and root.resolve(strict=True) == root and str(root) not in manifests,
                "Noncanonical/duplicate owner manifest root")
        raw = base64.b64decode(item["rawBase64"], validate=True)
        value = json.loads(raw)
        require(value == item["value"] and sha(raw) == item["manifestSHA256"]
                and (root / "package.json").read_bytes() == raw, "Actual owner manifest changed")
        (owner_copies / (str(index) + ".package.json")).write_bytes(raw)
        manifests[str(root)] = (item, value)
        manifest_seals.append({"root": str(root), "name": value["name"],
                               "version": value.get("version"), "bytes": len(raw), "sha256": sha(raw)})
    require(len(manifests) == 5, "Owner-context chain needs five actual manifests")
    installed_root = (repo / "frontend/node_modules").resolve(strict=True)
    require(discovery["scan"]["installedRoot"] == str(installed_root)
            and discovery["scan"]["directoriesVisited"] > 0
            and discovery["scan"]["manifestsVisited"] > 0, "Physical package scan incomplete")
    previous = str((repo / "frontend").resolve())
    for edge, version in zip(edges, versions):
        require(edge["ownerRoot"] == previous, "Owner is not previous actual target")
        owner, owner_value = manifests[edge["ownerRoot"]]
        target, target_value = manifests[edge["targetRoot"]]
        require(owner_value["name"] == edge["owner"] and owner_value.get("version") == edge["ownerVersion"]
                and owner["manifestSHA256"] == edge["ownerManifestSHA256"], "Actual owner identity differs")
        require(target_value["name"] == edge["requested"] and target_value["version"] == version
                and edge["targetVersion"] == version and target["manifestSHA256"] == edge["targetManifestSHA256"],
                "Actual target identity differs")
        require(edge["declarations"] and all(owner_value[item["field"]][edge["requested"]] == item["range"]
                for item in edge["declarations"]), "Actual declared edge changed")
        root = Path(edge["targetRoot"])
        require(root.is_relative_to(installed_root), "Owner dependency escaped installed frontend")
        if edge["requested"] in ("query-string", "decode-uri-component"):
            entry = Path(edge["actualEntry"])
            import_url = urllib.parse.urlsplit(edge["importURL"])
            require(import_url.scheme == "file" and import_url.netloc == ""
                    and import_url.query == "" and import_url.fragment == ""
                    and urllib.parse.urlunsplit(import_url) == edge["importURL"]
                    and urllib.parse.unquote_to_bytes(import_url.path) == os.fsencode(entry),
                    "Actual ESM import URL does not identify the exact canonical entry")
            require(target_value["exports"]["default"] == "./index.js"
                    and entry == root / "index.js" and entry.resolve(strict=True) == entry
                    and sha(entry.read_bytes()) == edge["actualEntrySHA256"], "Actual ESM entry binding changed")
        previous = edge["targetRoot"]
    instances = discovery["physicalInstances"]
    require(instances and len({item["root"] for item in instances}) == len(instances)
            and previous in {item["root"] for item in instances}, "Physical scan omitted actual decoder")
    request = urllib.request.Request(TARBALL, headers={"User-Agent": "sub2api-CI-decode1712-evidence"})
    with urllib.request.urlopen(request, timeout=60) as response:
        require(response.url == TARBALL, "Unexpected official tarball redirect")
        archive = response.read(1024 * 1024 + 1)
    require(len(archive) <= 1024 * 1024 and hashlib.sha1(archive).hexdigest() == SHA1
            and "sha512-" + base64.b64encode(hashlib.sha512(archive).digest()).decode() == SRI,
            "Official decoder tarball differs from frozen publisher integrity")
    archive_path = evidence / "official-decode-uri-component-0.5.1.tgz"
    archive_path.write_bytes(archive)
    published = {}
    with tarfile.open(archive_path, "r:gz") as package:
        for member in package.getmembers():
            name = PurePosixPath(member.name)
            require(name.parts and not name.is_absolute() and ".." not in name.parts and name.parts[0] == "package",
                    "Unsafe official package path")
            if member.isdir():
                continue
            require(member.isfile() and len(name.parts) > 1, "Unsupported official package member type")
            relative = PurePosixPath(*name.parts[1:]).as_posix()
            require(relative not in published, "Duplicate official published file")
            published[relative] = package.extractfile(member).read()
    require(len(published) == 5 and {"package.json", "index.js", "index.d.ts"} <= set(published),
            "Official decoder published file inventory differs")
    official = json.loads(published["package.json"])
    require(official["name"] == "decode-uri-component" and official["version"] == VERSION,
            "Official package identity differs")
    results = []
    for index, item in enumerate(instances):
        root = Path(item["root"])
        require(root.is_absolute() and root.resolve(strict=True) == root and root.is_relative_to(installed_root),
                "Noncanonical/outside installed decoder root")
        require(item["value"]["name"] == "decode-uri-component" and item["value"]["version"] == VERSION,
                "Old/unexpected physical installed decoder")
        files = {}
        for directory, dirs, names in os.walk(root, followlinks=False):
            require(all(not (Path(directory) / name).is_symlink() for name in dirs),
                    "Unexpected installed package directory symlink")
            for name in names:
                path = Path(directory) / name
                require(stat.S_ISREG(path.lstat().st_mode), "Unexpected installed member type")
                files[path.relative_to(root).as_posix()] = path
        require(set(files) == set(published), "Installed decoder published files missing/extra")
        target = evidence / "installed" / str(index)
        target.mkdir(parents=True, exist_ok=False)
        seals = []
        for name, expected in published.items():
            actual = files[name].read_bytes()
            require(actual == expected, "Installed decoder differs from official bytes: " + str(files[name]))
            copy = target / name
            copy.parent.mkdir(parents=True, exist_ok=True)
            copy.write_bytes(actual)
            seals.append({"path": name, "bytes": len(actual), "sha256": sha(actual)})
        require(sha(published["package.json"]) == item["manifestSHA256"], "Physical manifest changed")
        results.append({"root": str(root), "version": VERSION, "files": seals})
    return {"officialURL": TARBALL, "officialSRI": SRI, "officialSHA1": SHA1,
            "officialSHA256": sha(archive), "officialBytes": len(archive), "publishedFiles": len(published),
            "ownerManifestSeals": manifest_seals, "instances": results}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--evidence", required=True)
    parser.add_argument("--expected-head", required=True)
    args = parser.parse_args()
    require(os.environ.get("GITHUB_ACTIONS") == "true", "Only remote CI may execute this observation")
    repo = Path(__file__).resolve().parent.parent
    evidence = Path(args.evidence).resolve()
    require(evidence.is_absolute() and not evidence.is_relative_to(repo), "Evidence must be external")
    evidence.mkdir(parents=True, exist_ok=False)
    report = {"expectedHead": args.expected_head, "runID": os.environ.get("GITHUB_RUN_ID"),
              "runAttempt": os.environ.get("GITHUB_RUN_ATTEMPT"), "errors": [],
              "scope": "Actual installed owner-chain package bytes and eight real package contracts. No production exploit/HTTP reachability assertion or original gate replacement."}
    before = None
    try:
        before = snapshot(repo, args.expected_head, evidence, "source-before")
        require(not os.environ.get("NODE_OPTIONS") and not os.environ.get("NODE_PATH"),
                "External Node preload/search path forbidden")
        require(args.expected_head == os.environ["EXPECTED_SOURCE_HEAD"], "Event head binding differs")
        event_raw = Path(os.environ["GITHUB_EVENT_PATH"]).read_bytes()
        event = json.loads(event_raw)
        event_name = os.environ["GITHUB_EVENT_NAME"]
        if event_name == "pull_request":
            require(event["pull_request"]["head"]["sha"] == args.expected_head, "Raw PR event head differs")
        else:
            require(os.environ["GITHUB_SHA"] == args.expected_head, "Raw runner event head differs")
            if event_name == "push":
                require(event["after"] == args.expected_head, "Raw push event head differs")
        (evidence / "event.json").write_bytes(event_raw)
        package = (repo / "frontend/package.json").read_bytes()
        lock = (repo / "frontend/pnpm-lock.yaml").read_bytes()
        manifest = json.loads(package)
        require(manifest["pnpm"]["overrides"]["query-string@9.3.1>decode-uri-component"] == VERSION,
                "Scoped query-string decoder pin differs")
        require(lock.count(b"decode-uri-component@0.5.1:") == 2 and lock.count(SRI.encode()) == 1
                and b"decode-uri-component@0.4.1" not in lock and b"decode-uri-component@0.5.0" not in lock,
                "Frozen decoder lock identity differs")
        (evidence / "package.json").write_bytes(package)
        (evidence / "pnpm-lock.yaml").write_bytes(lock)
        report["packageSHA256"], report["lockSHA256"] = sha(package), sha(lock)
        report["nodeVersion"] = capture(evidence, "node-version", ["node", "--version"], repo).decode().strip()
        report["pnpmVersion"] = capture(evidence, "pnpm-version", ["pnpm", "--version"], repo).decode().strip()
        require(report["nodeVersion"].startswith("v22.") and report["pnpmVersion"] == "9.15.9",
                "Actual Node22/pnpm9.15.9 environment differs")
        probe = repo / "frontend/tools/decode-uri-component-contracts.mjs"
        (evidence / "executed-contracts.mjs").write_bytes(probe.read_bytes())
        (evidence / "executed-wrapper.py").write_bytes(Path(__file__).read_bytes())
        stdout = capture(evidence, "contracts", ["node", str(probe), str(evidence / "probe")], repo)
        contracts = json.loads((evidence / "probe/contracts.json").read_bytes())
        require(contracts["errors"] == [] and contracts["names"] == GROUPS
                and [item["name"] for item in contracts["groups"]] == GROUPS
                and all(item["status"] == "PASS" for item in contracts["groups"]),
                "Eight actual contract group results missing/failed/duplicate")
        events = [json.loads(line) for line in stdout.decode("utf-8").splitlines()]
        require(events == [event for name in GROUPS for event in
                           ({"name": name, "action": "RUN"}, {"name": name, "action": "PASS"})],
                "Actual contract RUN/PASS stream incomplete/duplicate")
        discovery = json.loads((evidence / "probe/discovery.json").read_bytes())
        report["packageBytes"] = installed_evidence(repo, evidence, discovery)
        child = json.loads((evidence / "probe/publisher-child.process.json").read_bytes())
        require(child["started"] and child["actualExit"] == 0 and child["signal"] is None
                and child["streamsComplete"] and not child["watchdog"]
                and not child["outputLimitExceeded"] and child["outputLimitBytes"] == 65536
                and not child["stdoutTruncated"] and not child["stderrTruncated"]
                and child["stdoutTotalBytes"] == child["stdoutBytes"]
                and child["stderrTotalBytes"] == child["stderrBytes"]
                and child["stdoutBytes"] + child["stderrBytes"] <= 65536
                and child["elapsedMilliseconds"] < child["timeoutMilliseconds"] == 10000,
                "Publisher payload did not naturally complete in its owned child")
        for stream in ("stdout", "stderr"):
            raw = (evidence / ("probe/publisher-child." + stream)).read_bytes()
            require(len(raw) == child[stream + "Bytes"] and sha(raw) == child[stream + "SHA256"],
                    "Actual child stream identity differs")
        require(child["stderrBytes"] == 0, "Unexpected actual child error output")
        report["contractGroups"] = contracts["groups"]
        report["childActual"] = child
    except Exception:
        report["errors"].append(traceback.format_exc())
    finally:
        try:
            after = snapshot(repo, args.expected_head, evidence, "source-after")
            require(before is not None and before == after, "Source changed or initial full freeze incomplete")
            report["sourceHead"], report["sourceTree"] = after["head"], after["tree"]
        except Exception:
            report["errors"].append(traceback.format_exc())
        report["actualExit"] = 1 if report["errors"] else 0
        save(evidence / "actual-decode-installation-and-contracts.json", report)
    print(json.dumps({"actualExit": report["actualExit"], "errors": report["errors"], "evidence": str(evidence)}))
    return report["actualExit"]


if __name__ == "__main__":
    sys.exit(main())


