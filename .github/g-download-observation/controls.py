"""Remote-only real Git / Go metadata controls. No project execution locally/87."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import observe as observer


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def run(source):
    require(os.environ.get("GITHUB_ACTIONS") == "true", "Run controls only in remote CI")
    destination = observer.E / "integrity-controls"
    destination.mkdir(parents=True, exist_ok=False)
    result = {"sourceHead": observer.git(source, "rev-parse", "HEAD").decode().strip(),
              "controlsSourceSHA256": observer.sha(Path(__file__).read_bytes()), "errors": []}
    try:
        result["inputs"] = input_controls(source, destination / "inputs")
    except Exception:
        import traceback
        result["errors"].append({"inputs": traceback.format_exc()})
    try:
        result["metadata"] = metadata_controls(source, destination / "metadata")
    except Exception:
        import traceback
        result["errors"].append({"metadata": traceback.format_exc()})
    observer.save(destination / "results.json", result)
    require(not result["errors"], "Actual integrity controls failed/incomplete")


def input_controls(source, destination):
    destination.mkdir()
    clean = destination / "clean"
    clean.mkdir()
    def git(root, *args, data=None):
        return subprocess.check_output(["git", "-c", "core.hooksPath=/dev/null", "-C", str(root), *args], input=data)
    git(clean, "init", "--quiet")
    git(clean, "config", "core.autocrlf", "false")
    inputs = {"frontend/shared fixture.txt": b"shared input\n", "backend/control.bin": b"\x00\xff\x01binary\x00",
              "backend/control.txt": b"tracked text\n", ".hidden-input": b"hidden\n", "frontend/Unicode-\u6d4b\u8bd5.txt": b"unicode name\n",
              ".gitignore": b"ignored.bin\n"}
    for name, raw in inputs.items():
        path = clean / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(raw)
    git(clean, "add", "--all")
    git(clean, "-c", "user.name=G metadata control", "-c", "user.email=g-control@example.invalid", "commit", "--quiet", "-m", "owned input fixture")
    cases = ("clean", "byte-change", "truncate", "remove", "staged-blob", "index-mode", "unmerged", "untracked", "ignored")
    diagnostics = {"byte-change": "Frozen input differs:", "truncate": "Frozen input differs:", "remove": "Frozen regular file type differs:",
                   "staged-blob": "Git index does not match frozen HEAD tree", "index-mode": "Git index does not match frozen HEAD tree",
                   "unmerged": "Unmerged/duplicate Git index input", "untracked": "Untracked/ignored input", "ignored": "Untracked/ignored input"}
    records = []
    checker = source / "backend/tools/req-http2-security-evidence.py"
    for name in cases:
        root = destination / ("case-" + name)
        shutil.copytree(clean, root)
        file = root / "backend/control.txt"
        if name == "byte-change": file.write_bytes(b"changed text\n")
        elif name == "truncate": (root / "backend/control.bin").write_bytes(b"\x00")
        elif name == "remove": file.unlink()
        elif name == "staged-blob":
            file.write_bytes(b"staged mismatch\n")
            git(root, "add", "backend/control.txt")
        elif name == "index-mode": git(root, "update-index", "--chmod=+x", "backend/control.txt")
        elif name == "unmerged":
            blob = git(root, "rev-parse", "HEAD:backend/control.txt").decode().strip()
            payload = ("0 " + "0" * 40 + "\tbackend/control.txt\n" + "100644 " + blob + " 1\tbackend/control.txt\n" + "100644 " + blob + " 2\tbackend/control.txt\n").encode()
            git(root, "update-index", "--index-info", data=payload)
        elif name == "untracked": (root / "untracked.bin").write_bytes(b"extra")
        elif name == "ignored": (root / "ignored.bin").write_bytes(b"extra ignored")
        proof = destination / ("proof-" + name)
        proof.mkdir()
        command = observer.capture(destination / ("command-" + name), [sys.executable, "-B", str(checker), "inputs", str(root), str(proof)], root)
        require(command["started"] and command["streamsComplete"] and not command["watchdog"], "Input control did not execute normally")
        stderr = (destination / ("command-" + name) / "stderr").read_text(encoding="utf-8")
        if name == "clean":
            observer.require_success(command)
            values = json.loads((proof / "full-repository-inputs.json").read_text(encoding="utf-8"))
            require({v["path"] for v in values} == set(inputs), "Input proof omitted real tracked file")
            require(all(v["sha256"] == hashlib.sha256(inputs[v["path"]]).hexdigest() for v in values), "Input proof bytes differ")
            modes = json.loads((proof / "full-repository-input-modes.json").read_text(encoding="utf-8"))
            require(modes["gitProcessCount"] == 10, "Input gate grew per-file Git commands")
        else:
            require(command["exitCode"] != 0 and diagnostics[name] in stderr, "Corrupt input escaped the original full Git gate")
            require(not (proof / "full-repository-inputs.json").exists(), "Failed input published a complete proof")
        records.append({"case": name, "actual": command, "diagnostic": stderr, "passed": True})
    return {"records": records, "actualCases": len(records), "checkerSHA256": observer.sha(checker.read_bytes()),
            "policy": "Real owned Git repositories and actual CLI; every tracked byte is hashed. No business timeout change."}


def metadata_controls(source, destination):
    destination.mkdir()
    project = destination / "project"
    project.mkdir()
    module = "example.org/gmeta"
    files = {"go.mod": b"module example.org/gmeta\n\ngo 1.26.10\n", "root.go": b"package metacontrol\nfunc RootAnchor() int { return 0 }\n",
             "linked/linked.go": b"package linked\nfunc ParentOnly() int { return 1 }\nfunc ChildOnly() int { return 2 }\n",
             "auxiliary/auxiliary.go": b"package auxiliary\nfunc NotLinked() int { return 7 }\n"}
    helper_source = source / "backend/third_party/req/download_wire_peer_test.go"
    text = helper_source.read_text(encoding="utf-8")
    start, end = text.index("// G_AUX_METADATA_HELPERS_BEGIN"), text.index("// G_AUX_METADATA_HELPERS_END")
    helpers = text[start:end]
    template = Path(__file__).with_name("metadata_control_test.go.txt").read_text(encoding="utf-8")
    require(template.count("// G_AUX_IMPLEMENTATION") == 1, "Metadata fixture projection marker differs")
    files["metadata_control_test.go"] = template.replace("// G_AUX_IMPLEMENTATION", helpers).encode()
    for name, raw in files.items():
        path = project / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(raw)
    observer.save(destination / "raw-fixture-seals.json", {name: observer.sha(raw) for name, raw in files.items()})
    (destination / "extracted-helper-source.go.txt").write_text(helpers, encoding="utf-8")
    # Formatting is remote only and affects this owned synthetic control module,
    # never application sources. Preserve the raw projection and actual results.
    shutil.copyfile(project / "metadata_control_test.go", destination / "raw-metadata-control.go.txt")
    observer.require_success(observer.capture(destination / "remote-format", ["gofmt", "-w", str(project / "metadata_control_test.go")], project))
    shutil.copyfile(project / "metadata_control_test.go", destination / "formatted-metadata-control.go.txt")
    output = destination / "output"
    output.mkdir()
    env = dict(os.environ, G_OBSERVATION_PHASE="metadata-controls", G_META_CONTROL_OUTPUT=str(output))
    wrapper = Path(observer.__file__).resolve()
    require('"' not in str(wrapper) and '"' not in sys.executable, "Unsafe actual -exec quoting")
    exec_flag = '"' + sys.executable + '" "' + str(wrapper) + '" exec'
    command = observer.capture(destination / "go-command", ["go", "test", "-p=1", "-parallel=1", "-race", "-count=1", "-timeout=2m", "-json", "-covermode=atomic", "-coverpkg=" + module + "/...", "-coverprofile=" + str(destination / "aggregate.raw.out"), "-exec=" + exec_flag, module + "/..."], project, timeout=300, env=env)
    observer.require_success(command)
    events = [json.loads(line) for line in (destination / "go-command/stdout").read_text(encoding="utf-8").splitlines()]
    require(not any(v["Action"] == "fail" or (v["Action"] == "skip" and v.get("Test")) for v in events), "Real metadata control Go event failed/skipped")
    require(sum(v["Action"] == "run" and v.get("Test") == "TestCoverageMetadataIsolation" for v in events) == sum(v["Action"] == "pass" and v.get("Test") == "TestCoverageMetadataIsolation" for v in events) == 1, "Real metadata control test missing/duplicate")
    identities = [(p.parent, json.loads(p.read_text(encoding="utf-8"))) for p in (observer.E / "phases/metadata-controls/binaries").glob("*/identity.json")]
    require(len(identities) == 1, "Metadata control exact binary identity missing/duplicate")
    binary_dir, identity = identities[0]
    require(identity["metadataComplete"] and identity["auxBefore"] == identity["auxAfter"], "Metadata control original auxiliary input changed")
    parent_profile = binary_dir / "parent-binary.raw.out"
    parent = observer.profile_blocks(parent_profile, project, module=module, module_root=project)
    aggregate = observer.profile_blocks(destination / "aggregate.raw.out", project, aggregate=True, module=module, module_root=project)
    require(parent == aggregate, "Real aggregate lost metadata definitions")
    require(len((destination / "aggregate.raw.out").read_text(encoding="utf-8").splitlines()) - 1 > len(aggregate), "Real Go aggregate duplicate-row control missing")
    parent_counts = row_counts(parent_profile)
    records = json.loads((output / "results.json").read_text(encoding="utf-8"))
    require([v["Name"] for v in records] == ["positive", "missing-aux", "changed-collection", "changed-fragment", "wrong-association", "preseed"], "Metadata control cases incomplete")
    for item in records:
        name = item["Name"]
        require(not item["Watchdog"], "Metadata control watchdog is not a valid rejection")
        if name == "preseed":
            require(item["PrepareError"] and not item["Arguments"], "Foreign coverage directory was reused")
            continue
        require("META_CHILD_EXECUTED" in item["Output"] and "META_CHILD_BODY_DONE" in item["Output"], "Real child did not execute its body")
        require("-test.gocoverdir=" + item["CoverEnvironment"] in item["Arguments"], "Child flag/environment binding missing")
        if name == "positive":
            child = observer.profile_blocks(Path(item["Profile"]), project, module=module, module_root=project)
            require(child == parent and item["ActualExit"] == 0 and not item["VerifyError"], "Complete fresh child profile differs from actual same binary")
            counts = row_counts(Path(item["Profile"]))
            witnesses = set()
            for key, value in parent_counts.items():
                if key[0].endswith("/linked/linked.go") and key[1] == 2:
                    require(value > 0 and counts[key] == 0, "Parent counters contaminated child")
                    witnesses.add("parent-only")
                if key[0].endswith("/linked/linked.go") and key[1] == 3:
                    require(value == 0 and counts[key] > 0, "Child counters contaminated parent")
                    witnesses.add("child-only")
            require(witnesses == {"parent-only", "child-only"}, "Actual disjoint coverage witnesses missing")
            require(any(key[0].endswith("/auxiliary/auxiliary.go") and value == 0 for key, value in counts.items()), "Unlinked zero definition was not emitted by Go")
        elif name == "missing-aux":
            child = observer.profile_blocks(Path(item["Profile"]), project, module=module, module_root=project)
            require(child != parent and item["ActualExit"] == 0 and item["VerifyError"], "Missing zero definition was waived")
        elif name == "changed-fragment":
            require(item["ActualExit"] != 0 and item["VerifyError"], "Corrupt fragment escaped actual Go decoder/integrity gate")
        else:
            require(item["VerifyError"], "Changed auxiliary collection escaped integrity gate")
    parser = profile_parser_controls(project, module, parent_profile, destination)
    return {"actualGoCommand": command, "binarySHA256": identity["beforeSHA256"], "sourceHelperSHA256": observer.sha(helper_source.read_bytes()),
            "helperProjectionSHA256": observer.sha(helpers.encode()), "cases": records, "counterIsolation": "Disjoint actual parent-only/child-only branches; no profile merging",
            "actualAuxiliaryInputs": identity["auxBefore"], "parser": parser}


def row_counts(path):
    result = {}
    for line in path.read_text(encoding="utf-8").splitlines()[1:]:
        match = observer.re.fullmatch(r"(.+):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)", line)
        require(match is not None, "Control profile record malformed")
        result[(match[1], *map(int, match.groups()[1:5]))] = int(match[7])
    return result


def profile_parser_controls(project, module, actual, destination):
    raw = actual.read_bytes()
    lines = raw.decode().splitlines()
    original = observer.profile_blocks(actual, project, module=module, module_root=project)
    first = lines[1]
    prefix, statements, count = first.rsplit(" ", 2)
    copies = {"conflicting-statement": lines + [prefix + " " + str(int(statements) + 1) + " " + count],
              "negative-count": [lines[0], prefix + " " + statements + " -1"],
              "outside-source": [lines[0], module + "/root.go:9999.1,9999.2 1 0"],
              "same-binary-duplicate": lines + [first], "missing-definition": [lines[0]] + lines[2:]}
    records = []
    for name, content in copies.items():
        path = destination / ("negative-" + name + ".control-only.out")
        path.write_text("\n".join(content) + "\n", encoding="utf-8")
        rejected = False
        try:
            values = observer.profile_blocks(path, project, aggregate=name == "conflicting-statement", module=module, module_root=project)
            require(values == original, "Exact definition set differs")
        except AssertionError:
            rejected = True
        require(rejected, "Invalid profile control escaped: " + name)
        records.append({"case": name, "rejected": rejected, "controlCopySHA256": observer.sha(path.read_bytes())})
    require(actual.read_bytes() == raw, "Negative controls rewrote actual original profile")
    return {"records": records, "originalRawSHA256": observer.sha(raw), "policy": "Corruption copies only; not runtime coverage, no fabricated accepted hits."}


if __name__ == "__main__":
    run(Path(sys.argv[1]).resolve())
