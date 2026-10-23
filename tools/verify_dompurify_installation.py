#!/usr/bin/env python3
"""Remote CI only: preserve real DOMPurify resolution and published package bytes."""
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
import urllib.request

VERSION = "3.4.17"
VITEST_VERSION = "2.1.9"
JSDOM_VERSION = "24.1.3"
TARBALL = "https://registry.npmjs.org/dompurify/-/dompurify-3.4.17.tgz"
SRI = "sha512-B/pSlYVHQ58kqISD/yDjuJ1yfzI34m1gF4lAod2bwSRhiN4K5Cl3H8Af7hqB2PmTL911tdEudnnXn5FnhRoz0w=="
SHA1 = "074bbb24eaa419535ac19ffe7f663104b0e1543d"

# Node's real owner-context search paths locate package manifests without asking
# packages to export a package.json subpath. DOMPurify and the Vitest/jsdom
# environment have require entries observed; no dependency module is evaluated.
DISCOVER = r"""
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const { createRequire } = require('node:module');
const frontend = fs.realpathSync(process.argv[1]);
const digest = raw => crypto.createHash('sha256').update(raw).digest('hex');
function metadata(directory) {
  const root = fs.realpathSync(directory);
  const raw = fs.readFileSync(path.join(root, 'package.json'));
  const value = JSON.parse(raw);
  return {root, value, manifestSHA256: digest(raw), manifestBase64: raw.toString('base64')};
}
function edge(owner, name) {
  const request = createRequire(path.join(owner.root, 'package.json'));
  const declarations = ['dependencies', 'devDependencies', 'optionalDependencies', 'peerDependencies']
    .filter(key => owner.value[key] && Object.hasOwn(owner.value[key], name))
    .map(key => ({field: key, range: owner.value[key][name]}));
  if (!declarations.length) throw new Error('Missing actual declared owner edge: ' + name);
  const search = request.resolve.paths(name);
  if (!Array.isArray(search)) throw new Error('No Node resolution search paths: ' + name);
  for (const base of search) {
    const candidate = path.join(base, name, 'package.json');
    if (!fs.existsSync(candidate)) continue;
    const target = metadata(path.dirname(candidate));
    if (target.value.name !== name) throw new Error('Resolved package name mismatch: ' + candidate);
    const record = {owner: owner.value.name, ownerVersion: owner.value.version ?? null, ownerRoot: owner.root, ownerManifestSHA256: owner.manifestSHA256,
      requested: name, declarations, nodeSearchPaths: search, targetRoot: target.root,
      targetManifestSHA256: target.manifestSHA256, targetVersion: target.value.version};
    if (['dompurify', 'vitest', 'jsdom'].includes(name)) {
      record.actualEntry = fs.realpathSync(request.resolve(name));
      if (!record.actualEntry.startsWith(target.root + path.sep)) throw new Error('Resolved require entry escapes package: ' + name);
      record.actualEntrySHA256 = digest(fs.readFileSync(record.actualEntry));
    }
    if (name === 'dompurify') {
      record.moduleEntryInterpretation = 'Published manifest.module field; not actual ESM import resolution or execution';
      record.moduleEntry = fs.realpathSync(path.resolve(target.root, target.value.module));
      if (!record.moduleEntry.startsWith(target.root + path.sep)) throw new Error('DOMPurify module entry escapes package');
      record.moduleEntrySHA256 = digest(fs.readFileSync(record.moduleEntry));
    }
    return {target, record};
  }
  throw new Error('Actual installed owner edge missing: ' + name);
}
const root = metadata(frontend);
const direct = edge(root, 'dompurify');
const icons = edge(root, '@lobehub/icons');
const ui = edge(icons.target, '@lobehub/ui');
const mermaid = edge(ui.target, 'mermaid');
const nested = edge(mermaid.target, 'dompurify');
const types = edge(root, '@types/dompurify');
const typed = edge(types.target, 'dompurify');
const vitest = edge(root, 'vitest');
const testDom = edge(vitest.target, 'jsdom');
const directDom = edge(root, 'jsdom');
const testEnvironment = {name: 'vitest-jsdom', edges: [vitest.record, testDom.record],
  directJsdom: directDom.record, interpretation: 'Actual installed owner-context require resolution; not Vitest or browser execution'};
const logical = [
  {name: 'direct', edges: [direct.record]},
  {name: 'mermaid', edges: [icons.record, ui.record, mermaid.record, nested.record]},
  {name: 'types-stub', edges: [types.record, typed.record]},
];
const physical = new Map();
for (const item of [direct.target, nested.target, typed.target]) physical.set(item.root, item);
let directoriesVisited = 0, moduleManifests = 0;
const seen = new Set();
function inspectCandidate(candidate) {
  if (!fs.existsSync(path.join(candidate, 'package.json'))) return;
  const item = metadata(candidate);
  moduleManifests++;
  if (item.value.name === 'dompurify') physical.set(item.root, item);
}
function walk(directory) {
  const real = fs.realpathSync(directory);
  if (seen.has(real)) return;
  seen.add(real);
  directoriesVisited++;
  const entries = fs.readdirSync(directory, {withFileTypes: true});
  if (path.basename(directory) === 'node_modules') {
    for (const item of entries) {
      if (item.name.startsWith('.')) continue;
      const candidate = path.join(directory, item.name);
      if (item.name.startsWith('@')) {
        for (const scoped of fs.readdirSync(candidate)) inspectCandidate(path.join(candidate, scoped));
      } else if (item.isDirectory() || item.isSymbolicLink()) inspectCandidate(candidate);
    }
  }
  for (const item of entries) {
    // Every physical virtual-store directory is traversed, including .pnpm.
    // Symlink aliases are inspected above, never recursively followed into cycles.
    if (item.isDirectory() && !item.isSymbolicLink()) walk(path.join(directory, item.name));
  }
}
const logicalManifests = new Map();
for (const item of [root, icons.target, ui.target, mermaid.target, types.target, direct.target, nested.target, typed.target, vitest.target, testDom.target, directDom.target]) {
  logicalManifests.set(item.root, {root: item.root, name: item.value.name, version: item.value.version ?? null,
    sha256: item.manifestSHA256, rawBase64: item.manifestBase64});
}
const installedRoot = fs.realpathSync(path.join(frontend, 'node_modules'));
walk(installedRoot);
console.log(JSON.stringify({logicalPaths: logical, testEnvironment, logicalManifests: [...logicalManifests.values()], physicalInstances: [...physical.values()].map(item =>
  ({root: item.root, name: item.value.name, version: item.value.version, manifestSHA256: item.manifestSHA256})),
  scan: {installedRoot, directoriesVisited, moduleManifests, followsDirectorySymlinks: false}}, null, 2));
"""


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
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            stdout, stderr = process.communicate(timeout=30)
        record.update({"actualExit": process.returncode, "streamsComplete": True})
    except Exception:
        record["captureError"] = traceback.format_exc()
        if process is not None and process.poll() is None:
            try:
                os.killpg(process.pid, signal.SIGKILL)
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


def inspect_package(evidence, discovery):
    require([p["name"] for p in discovery["logicalPaths"]] == ["direct", "mermaid", "types-stub"],
            "Three actual logical paths missing/reordered")
    manifests = {}
    manifest_seals = []
    manifest_directory = evidence / "logical-owner-manifests"
    manifest_directory.mkdir(exist_ok=False)
    for index, item in enumerate(discovery["logicalManifests"]):
        require(item["root"] not in manifests, "Duplicate logical owner manifest")
        data = base64.b64decode(item["rawBase64"], validate=True)
        value = json.loads(data)
        require(sha(data) == item["sha256"] and value["name"] == item["name"]
                and value.get("version") == item["version"], "Logical owner manifest identity differs")
        require((Path(item["root"]) / "package.json").read_bytes() == data,
                "Logical owner manifest changed after discovery")
        (manifest_directory / (str(index) + ".package.json")).write_bytes(data)
        manifests[item["root"]] = (item, value)
        manifest_seals.append({"root": item["root"], "name": item["name"], "version": item["version"],
                               "bytes": len(data), "sha256": sha(data), "copy": str(index) + ".package.json"})
    logical_roots = set()
    chains = [["dompurify"], ["@lobehub/icons", "@lobehub/ui", "mermaid", "dompurify"],
              ["@types/dompurify", "dompurify"]]
    environment = discovery["testEnvironment"]
    require(environment["name"] == "vitest-jsdom", "Actual DOM test environment chain missing")
    checks = list(zip(discovery["logicalPaths"], chains)) + [
        (environment, ["vitest", "jsdom"]),
        ({"edges": [environment["directJsdom"]]}, ["jsdom"])]
    for path, chain in checks:
        require([edge["requested"] for edge in path["edges"]] == chain, "Logical package chain differs")
        previous = str(Path(__file__).resolve().parent.parent / "frontend")
        for edge in path["edges"]:
            require(edge["ownerRoot"] == previous, "Logical owner context is not the preceding actual target")
            owner, owner_value = manifests[edge["ownerRoot"]]
            target, target_value = manifests[edge["targetRoot"]]
            require(owner["name"] == edge["owner"] and owner["version"] == edge["ownerVersion"]
                    and owner["sha256"] == edge["ownerManifestSHA256"], "Actual owner manifest binding differs")
            require(target["name"] == edge["requested"] and target["version"] == edge["targetVersion"]
                    and target["sha256"] == edge["targetManifestSHA256"], "Actual target manifest binding differs")
            require(edge["declarations"] and all(owner_value[d["field"]][edge["requested"]] == d["range"]
                    for d in edge["declarations"]), "Declared dependency differs from raw actual manifest")
            if edge["requested"] in ("dompurify", "vitest", "jsdom"):
                entry = Path(edge["actualEntry"])
                require(entry.is_relative_to(Path(edge["targetRoot"])) and entry.resolve(strict=True) == entry
                        and sha(entry.read_bytes()) == edge["actualEntrySHA256"],
                        "Actual owner-context require entry changed")
            previous = edge["targetRoot"]
        final = path["edges"][-1]
        if final["requested"] == "dompurify":
            entry = Path(final["moduleEntry"])
            require(entry.is_relative_to(Path(final["targetRoot"])) and entry.resolve(strict=True) == entry
                    and sha(entry.read_bytes()) == final["moduleEntrySHA256"], "Published module-field entry changed")
            require(final["moduleEntryInterpretation"] ==
                    "Published manifest.module field; not actual ESM import resolution or execution",
                    "Published module-field evidence interpretation differs")
            require(final["targetVersion"] == VERSION, "Reachable DOMPurify is not patched")
            logical_roots.add(final["targetRoot"])
        else:
            require(final["requested"] == "jsdom" and final["targetVersion"] == JSDOM_VERSION,
                    "Actual jsdom differs from frozen test environment")
    require(environment["edges"][0]["targetVersion"] == VITEST_VERSION,
            "Actual Vitest differs from frozen test environment")
    require(environment["edges"][-1]["targetRoot"] == environment["directJsdom"]["targetRoot"],
            "Vitest and frontend resolve different actual jsdom instances")
    installed_root = Path(discovery["scan"]["installedRoot"]).resolve(strict=True)
    for edge in environment["edges"] + [environment["directJsdom"]]:
        root = Path(edge["targetRoot"])
        require(root.is_absolute() and root.resolve(strict=True) == root and root.is_relative_to(installed_root),
                "DOM test environment resolves outside actual project installation")
    instances = discovery["physicalInstances"]
    require(instances and len({p["root"] for p in instances}) == len(instances),
            "Missing/duplicate physical instance records")
    require(logical_roots <= {p["root"] for p in instances}, "Physical evidence omitted a logical instance")
    require(all(p["name"] == "dompurify" and p["version"] == VERSION for p in instances),
            "Old/unexpected installed DOMPurify instance")
    require(discovery["scan"]["directoriesVisited"] > 0 and discovery["scan"]["moduleManifests"] > 0,
            "Physical installed module scan was incomplete")
    request = urllib.request.Request(TARBALL, headers={"User-Agent": "sub2api-CI-DOMPurify1749-evidence"})
    with urllib.request.urlopen(request, timeout=60) as response:
        require(response.url == TARBALL, "Unexpected official tarball redirect")
        archive = response.read(16 * 1024 * 1024 + 1)
    require(len(archive) <= 16 * 1024 * 1024, "Official package exceeded observation bound")
    require(hashlib.sha1(archive).hexdigest() == SHA1 and
            "sha512-" + base64.b64encode(hashlib.sha512(archive).digest()).decode() == SRI,
            "Official package integrity differs from frozen lock")
    tar_path = evidence / "official-dompurify-3.4.17.tgz"
    tar_path.write_bytes(archive)
    published = {}
    with tarfile.open(tar_path, "r:gz") as package:
        for member in package.getmembers():
            name = PurePosixPath(member.name)
            require(name.parts and not name.is_absolute() and ".." not in name.parts and name.parts[0] == "package",
                    "Unsafe official tar member path")
            if member.isdir():
                continue
            require(member.isfile() and len(name.parts) > 1, "Unsupported official tar member type")
            relative = PurePosixPath(*name.parts[1:]).as_posix()
            require(relative not in published, "Duplicate official tar member")
            published[relative] = package.extractfile(member).read()
    require(len(published) == 24, "Official published file inventory differs")
    official = json.loads(published["package.json"])
    require(official["name"] == "dompurify" and official["version"] == VERSION,
            "Official package identity differs")
    results = []
    installed_root = Path(discovery["scan"]["installedRoot"]).resolve(strict=True)
    for index, item in enumerate(instances):
        root = Path(item["root"])
        require(root.is_relative_to(installed_root), "DOMPurify resolved outside actual installed project modules")
        require(root.is_absolute() and root.resolve(strict=True) == root, "Noncanonical physical root")
        actual_files = {}
        for directory, dirs, files in os.walk(root, followlinks=False):
            for name in dirs:
                require(not (Path(directory) / name).is_symlink(), "Unexpected package directory symlink")
            for name in files:
                path = Path(directory) / name
                require(stat.S_ISREG(path.lstat().st_mode), "Unexpected package member type")
                actual_files[path.relative_to(root).as_posix()] = path
        require(set(actual_files) == set(published), "Installed package has missing/extra published files")
        member_seals = []
        copy = evidence / "installed" / str(index)
        copy.mkdir(parents=True, exist_ok=False)
        for name, expected in published.items():
            data = actual_files[name].read_bytes()
            require(data == expected, "Installed package byte mismatch: " + str(actual_files[name]))
            target = copy / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)
            member_seals.append({"path": name, "bytes": len(data), "sha256": sha(data)})
        require(sha(published["package.json"]) == item["manifestSHA256"], "Discovered instance manifest changed")
        results.append({**item, "archiveCopy": str(copy), "members": member_seals})
    return {"officialURL": TARBALL, "officialBytes": len(archive), "officialSHA1": SHA1,
            "officialSRI": SRI, "officialSHA256": sha(archive), "publishedFiles": len(published),
            "logicalManifestSeals": manifest_seals, "instances": results}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--evidence", required=True)
    parser.add_argument("--expected-head", required=True)
    args = parser.parse_args()
    require(os.environ.get("GITHUB_ACTIONS") == "true", "This observation may run only in remote CI")
    repo = Path(__file__).resolve().parent.parent
    evidence = Path(args.evidence).resolve()
    require(evidence.is_absolute() and not evidence.is_relative_to(repo), "Evidence must be external")
    evidence.mkdir(parents=True, exist_ok=False)
    report = {"expectedHead": args.expected_head, "runID": os.environ.get("GITHUB_RUN_ID"),
              "runAttempt": os.environ.get("GITHUB_RUN_ATTEMPT"), "errors": [], "scope":
              "Installed package identity and bytes only; not browser script execution, new-advisory exploitation or coverage85."}
    before = None
    try:
        before = snapshot(repo, args.expected_head, evidence, "source-before")
        report["observationEnvironment"] = {key: os.environ.get(key) for key in ("NODE_OPTIONS", "NODE_PATH", "CI", "GITHUB_SHA", "EXPECTED_SOURCE_HEAD")}
        require(not os.environ.get("NODE_OPTIONS") and not os.environ.get("NODE_PATH"), "Unexpected external Node preload/search path")
        require(args.expected_head == os.environ["EXPECTED_SOURCE_HEAD"], "Event-head binding differs")
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
        report["event"] = {"name": event_name, "ref": os.environ.get("GITHUB_REF"),
                           "runnerSHA": os.environ.get("GITHUB_SHA"), "rawEventSHA256": sha(event_raw)}
        package = (repo / "frontend/package.json").read_bytes()
        lock = (repo / "frontend/pnpm-lock.yaml").read_bytes()
        vitest_config = (repo / "frontend/vitest.config.ts").read_bytes()
        manifest = json.loads(package)
        require(manifest["devDependencies"]["vitest"] == "^" + VITEST_VERSION and
                manifest["devDependencies"]["jsdom"] == "^" + JSDOM_VERSION,
                "Declared DOM test environment differs")
        require(manifest["dependencies"]["dompurify"] == "^" + VERSION and
                manifest["pnpm"]["overrides"]["dompurify@<" + VERSION] == ">=" + VERSION,
                "Direct dependency/override floor differs")
        require(lock.count(("dompurify@" + VERSION + ":").encode()) == 2 and
                lock.count(SRI.encode()) == 1 and b"dompurify@3.4.15" not in lock,
                "Frozen lock identity differs")
        (evidence / "package.json").write_bytes(package)
        (evidence / "pnpm-lock.yaml").write_bytes(lock)
        (evidence / "vitest.config.ts").write_bytes(vitest_config)
        report["testEnvironmentInputs"] = {"lockSHA256": sha(lock), "vitestConfigSHA256": sha(vitest_config),
                                           "expectedVitest": VITEST_VERSION, "expectedJsdom": JSDOM_VERSION,
                                           "interpretation": "Current HEAD raw lock/config hashes are evidence, not fixed-value gates; installed identities checked separately"}
        report["nodeVersion"] = capture(evidence, "node-version", ["node", "--version"], repo).decode().strip()
        report["pnpmVersion"] = capture(evidence, "pnpm-version", ["pnpm", "--version"], repo).decode().strip()
        require(report["nodeVersion"].startswith("v20.") and report["pnpmVersion"].startswith("9."),
                "Unexpected actual Node/pnpm major version")
        (evidence / "executed-resolution.cjs").write_text(DISCOVER, encoding="utf-8")
        discovery = json.loads(capture(evidence, "resolution", ["node", "-e", DISCOVER, str(repo / "frontend")], repo))
        save(evidence / "discovery.json", discovery)
        report["logicalPaths"] = discovery["logicalPaths"]
        report["testEnvironment"] = discovery["testEnvironment"]
        report["scan"] = discovery["scan"]
        report["packageBytes"] = inspect_package(evidence, discovery)
    except Exception:
        report["errors"].append(traceback.format_exc())
    finally:
        try:
            after = snapshot(repo, args.expected_head, evidence, "source-after")
            require(before is not None and before == after, "Tracked source changed or initial snapshot incomplete")
            report["sourceHead"], report["sourceTree"] = after["head"], after["tree"]
        except Exception:
            report["errors"].append(traceback.format_exc())
        report["actualExit"] = 1 if report["errors"] else 0
        save(evidence / "actual-installation-receipt.json", report)
    print(json.dumps({"actualExit": report["actualExit"], "errors": report["errors"], "evidence": str(evidence)}))
    return report["actualExit"]


if __name__ == "__main__":
    sys.exit(main())
