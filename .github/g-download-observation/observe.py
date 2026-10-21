"""Remote CI evidence runner. Never invoke against the project locally or on 87.

Uses standard Go execution and preserves raw results. No accepted-RED list, source
rewriting, profile merging, fabricated events or production implementation.
"""
import collections
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import tarfile
import traceback
import uuid

BASE = "2d6bf53dc3231dc4ccfce48211c43942ed18d656"
SOURCE = "aa656edd6f2aed8c595368ee802516fa571e317d"
TREE = "ae5ad3301926ca2681a7ddc4e1b875838426ce74"
MODULE = "github.com/imroc/req/v3"
G_FILES = {
    "DOWNLOAD-CONTRACTS-LICENSE", "DOWNLOAD-CONTRACTS-PROVENANCE.json",
    "DOWNLOAD-CONTRACTS-REVIEW.md", "download_contract_test.go",
    "download_wire_peer_test.go", "parallel_download_contract_test.go",
    "parallel_download_contract_windows_test.go",
}
E = Path(os.environ["G_OBSERVATION_EVIDENCE"]).resolve()
WORKSPACE = Path(os.environ["GITHUB_WORKSPACE"]).resolve()


def sha(data):
    return hashlib.sha256(data).hexdigest()


def save(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")


def git(root, *arguments):
    return subprocess.check_output(["git", "-C", str(root), *arguments])


def capture(directory, argv, cwd, timeout=300, env=None, isolate=True):
    """Capture actual bytes/status, including launch and bounded termination."""
    directory.mkdir(parents=True, exist_ok=False)
    record = {"argv": list(map(str, argv)), "cwd": str(cwd),
              "startedAt": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "started": False, "exitCode": None, "watchdog": False, "streamsComplete": False}
    save(directory / "process.json", record)
    options = {}
    if isolate:
        if os.name == "nt":
            options["creationflags"] = subprocess.CREATE_NEW_PROCESS_GROUP
        else:
            options["start_new_session"] = True
    stdout, stderr = b"", b""
    try:
        process = subprocess.Popen(record["argv"], cwd=cwd, env=env,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                   **options)
        record["started"] = True
        record["pid"] = process.pid
        try:
            stdout, stderr = process.communicate(timeout=timeout)
            record["streamsComplete"] = True
        except subprocess.TimeoutExpired:
            record["watchdog"] = True
            try:
                if os.name == "nt":
                    killed = subprocess.run(["taskkill", "/PID", str(process.pid), "/T", "/F"],
                                            capture_output=True, check=False, timeout=30)
                    (directory / "termination.stdout").write_bytes(killed.stdout)
                    (directory / "termination.stderr").write_bytes(killed.stderr)
                    record["terminationExit"] = killed.returncode
                elif isolate:
                    os.killpg(process.pid, signal.SIGKILL)
                else:
                    process.kill()
            except (OSError, subprocess.TimeoutExpired) as error:
                record["terminationError"] = repr(error)
            try:
                stdout, stderr = process.communicate(timeout=30)
                record["streamsComplete"] = True
            except subprocess.TimeoutExpired as error:
                stdout, stderr = error.output or b"", error.stderr or b""
                record["terminationIncomplete"] = True
        record["exitCode"] = process.poll()
    except OSError as error:
        record["launchError"] = repr(error)
    (directory / "stdout").write_bytes(stdout)
    (directory / "stderr").write_bytes(stderr)
    record.update({"stdoutBytes": len(stdout), "stdoutSHA256": sha(stdout),
                   "stderrBytes": len(stderr), "stderrSHA256": sha(stderr),
                   "finishedAt": datetime.datetime.now(datetime.timezone.utc).isoformat()})
    save(directory / "process.json", record)
    return record


def successful(record):
    return record["started"] and record["exitCode"] == 0 and not record["watchdog"] and record["streamsComplete"]


def require_success(record):
    if not successful(record):
        raise RuntimeError("Actual command failed: " + json.dumps(record))


def snapshot(root, head, label, archive=False):
    """Read all tracked bytes against Git; archive all files without filtering."""
    assert git(root, "rev-parse", "HEAD").decode().strip() == head
    status = git(root, "status", "--porcelain=v1", "--untracked-files=all")
    (E / (label + ".status")).write_bytes(status)
    assert not status, "Dirty or incomplete full checkout: " + label
    index = git(root, "ls-files", "--stage", "-z")
    (E / (label + ".index")).write_bytes(index)
    files = []
    for row in index.split(b"\0"):
        if not row:
            continue
        header, name = row.split(b"\t", 1)
        mode, blob, stage = header.decode().split()
        assert stage == "0" and mode in ("100644", "100755", "120000")
        path = root / os.fsdecode(name)
        if mode == "120000":
            assert path.is_symlink()
            raw = os.fsencode(os.readlink(path))
        else:
            assert path.is_file() and not path.is_symlink()
            raw = path.read_bytes()
            if os.name != "nt":
                assert bool(path.stat().st_mode & 0o111) == (mode == "100755")
        assert hashlib.sha1(b"blob " + str(len(raw)).encode() + b"\0" + raw).hexdigest() == blob
        files.append({"path": os.fsdecode(name), "mode": mode, "blob": blob,
                      "sha256": sha(raw), "bytes": len(raw)})
    result = {"head": head, "tree": git(root, "rev-parse", "HEAD^{tree}").decode().strip(),
              "files": files, "modePolicy": "Git index modes; actual executable bit checked on POSIX"}
    save(E / (label + ".json"), result)
    if archive:
        target = E / (label + "-full-input.tar")
        with tarfile.open(target, "w") as package:
            for item in files:
                package.add(root / item["path"], arcname=item["path"], recursive=False)
        with tarfile.open(target, "r") as package:
            members = package.getmembers()
            assert len(members) == len(files)
            for member, item in zip(members, files):
                assert member.name == item["path"]
                raw = os.fsencode(member.linkname) if member.issym() else package.extractfile(member).read()
                assert len(raw) == item["bytes"] and sha(raw) == item["sha256"]
        save(E / (label + "-archive.json"), {"bytes": target.stat().st_size,
                                            "sha256": sha(target.read_bytes()),
                                            "trackedFiles": len(files)})
    return result


def exec_mode(argv, timeout=1000):
    """Standard go test -exec: run supplied binary once, forward real streams/status."""
    phase = os.environ["G_OBSERVATION_PHASE"]
    directory = E / "phases" / phase / "binaries" / uuid.uuid4().hex
    directory.mkdir(parents=True, exist_ok=False)
    binary = Path(argv[0]).resolve()
    identity = {"binary": str(binary),
                "argv": argv, "phase": phase, "source": SOURCE,
                "environment": {key: os.environ.get(key) for key in
                                ("GOOS", "GOARCH", "CGO_ENABLED", "GOTOOLCHAIN", "GOWORK",
                                 "GOFLAGS", "GOMAXPROCS", "GORACE", "SUB2API_G_DOWNLOAD_PROFILE_DIR", "TMP", "TEMP", "TMPDIR", "GOTMPDIR")}}
    try:
        before = binary.read_bytes()
        identity.update({"beforeSHA256": sha(before), "bytes": len(before)})
    except OSError as error:
        identity["binaryReadError"] = repr(error)
    save(directory / "identity.json", identity)
    result = capture(directory / "actual", argv, Path.cwd(), timeout=timeout, isolate=False)
    # Preserve original bytes before metadata failures can affect the wrapper exit.
    sys.stdout.buffer.write((directory / "actual/stdout").read_bytes())
    sys.stderr.buffer.write((directory / "actual/stderr").read_bytes())
    sys.stdout.buffer.flush()
    sys.stderr.buffer.flush()
    metadata_ok = False
    try:
        identity["afterSHA256"] = sha(binary.read_bytes())
        assert identity["beforeSHA256"] == identity["afterSHA256"]
        for label, command in (("go-version-m", ["go", "version", "-m", str(binary)]),
                               ("go-buildid", ["go", "tool", "buildid", str(binary)])):
            require_success(capture(directory / label, command, Path.cwd()))
        profile_flags = [value.split("=", 1)[1] for value in argv[1:]
                         if value.startswith("-test.coverprofile=")]
        assert len(profile_flags) == 1, "Actual test binary coverprofile argument missing/duplicate"
        profile = Path(profile_flags[0])
        if profile.is_file():
            raw = profile.read_bytes()
            (directory / "parent-binary.raw.out").write_bytes(raw)
            identity["parentProfile"] = {"originalPath": str(profile), "sha256": sha(raw), "bytes": len(raw)}
        else:
            identity["parentProfileError"] = "No actual runtime-produced parent profile"
        metadata_ok = "parentProfile" in identity
    except Exception:
        identity["metadataError"] = traceback.format_exc()
    identity["metadataComplete"] = metadata_ok
    save(directory / "identity.json", identity)
    # A known nonzero/signal has priority over missing metadata or watchdog flags.
    # Its exact original status/streams remain in actual/process.json regardless.
    if result["started"] and result["exitCode"] is not None and result["exitCode"] != 0:
        if os.name == "nt":
            import ctypes
            exit_process = ctypes.WinDLL("kernel32", use_last_error=True).ExitProcess
            exit_process.argtypes = [ctypes.c_uint32]
            exit_process.restype = None
            exit_process(ctypes.c_uint32(result["exitCode"]).value)
        if result["exitCode"] < 0 and os.name != "nt":
            signum = -result["exitCode"]
            if signum not in (signal.SIGKILL, signal.SIGSTOP):
                signal.signal(signum, signal.SIG_DFL)
            os.kill(os.getpid(), signum)
        return result["exitCode"]
    if not successful(result) or not metadata_ok:
        return 1  # Metadata failure may only turn actual zero/unknown into failure.
    return 0


def objects(raw):
    decoder, offset, values = json.JSONDecoder(), 0, []
    text = raw.decode()
    while offset < len(text):
        if text[offset].isspace():
            offset += 1
            continue
        value, offset = decoder.raw_decode(text, offset)
        values.append(value)
    return values


def module_graph(baseline, candidate):
    graphs = []
    roots = (("baseline", baseline), ("candidate", candidate))
    for label, root in roots:
        command = capture(E / "commands" / (label + "-modules"),
                          ["go", "list", "-m", "-json", "all"], root / "backend")
        require_success(command)
        values = objects((E / "commands" / (label + "-modules") / "stdout").read_bytes())
        assert len(values) == 365 and len({item["Path"] for item in values}) == 365
        assert all(isinstance(item["Path"], str) and item["Path"] for item in values)
        assert sum(item.get("Main") is True for item in values) == 1
        assert sum(item["Path"] == MODULE for item in values) == 1
        graphs.append(values)
    # Check raw graph path/order and actual external locations before normalization.
    assert [item["Path"] for item in graphs[0]] == [item["Path"] for item in graphs[1]]
    cache_presence_changes = []
    for before, after in zip(*graphs):
        if not before.get("Main") and before["Path"] != MODULE:
            for item in (before, after):
                if "Dir" in item:
                    assert isinstance(item["Dir"], str) and item["Dir"] and Path(item["Dir"]).is_absolute()
            if "Dir" in before and "Dir" in after:
                assert before["Dir"] == after["Dir"], "Actual external cache Dir moved: " + before["Path"]
            elif ("Dir" in before) != ("Dir" in after):
                cache_presence_changes.append({"path": before["Path"], "baselineHasDir": "Dir" in before,
                                               "candidateHasDir": "Dir" in after,
                                               "baselineDir": before.get("Dir"), "candidateDir": after.get("Dir")})
    normalized_graphs = []
    for (_, root), values in zip(roots, graphs):
        normalized = []
        for value in values:
            item = dict(value)
            if item.get("Main") or item["Path"] == MODULE:
                expected = root / "backend" if item.get("Main") else root / "backend/third_party/req"
                for key, location in (("Dir", expected), ("GoMod", expected / "go.mod")):
                    if key in item:
                        assert Path(item[key]).resolve() == location
                        item[key] = "CHECKOUT/" + location.relative_to(root).as_posix()
                if "Replace" in item:
                    replacement = dict(item["Replace"])
                    assert replacement["Path"] == "./third_party/req"
                    for key, location in (("Dir", expected), ("GoMod", expected / "go.mod")):
                        assert Path(replacement[key]).resolve() == location
                        replacement[key] = "CHECKOUT/" + location.relative_to(root).as_posix()
                    item["Replace"] = replacement
            else:
                assert "Replace" not in item and item.get("Version")
                # Only after exact raw Dir equality or recorded presence difference.
                item.pop("Dir", None)
            normalized.append(item)
        normalized_graphs.append(normalized)
    assert normalized_graphs[0] == normalized_graphs[1], "Actual secured main-MVS differs"
    for name in ("go.mod", "go.sum"):
        assert (baseline / "backend" / name).read_bytes() == (candidate / "backend" / name).read_bytes()
    save(E / "mvs-comparison.json", {"actualModules": 365,
                                    "rawPathOrderIdentical": True,
                                    "externalDirsExactWhenBothPresent": True,
                                    "optionalExternalDirPresenceChanges": cache_presence_changes,
                                    "rawGraphSHA256": {label: sha((E / "commands" / (label + "-modules") / "stdout").read_bytes()) for label, _ in roots},
                                    "allOtherFieldsPreservedAndEqual": True})



def compiler_preflight(actual_env):
    record = {"goCCBinding": actual_env["CC"]}
    try:
        assert record["goCCBinding"] == "gcc", "Unexpected Go CC binding"
        located = shutil.which(record["goCCBinding"])
        record["whichPath"] = located
        assert located, "Go CC executable cannot be resolved on actual PATH"
        resolved = Path(located).resolve(strict=True)
        record.update({"resolvedPath": str(resolved), "binarySHA256": sha(resolved.read_bytes())})
        require_success(capture(E / "commands/gcc-version", [located, "--version"], WORKSPACE))
        record["versionStdoutSHA256"] = sha((E / "commands/gcc-version/stdout").read_bytes())
        if os.name == "nt":
            require_success(capture(E / "commands/gcc-race-library", [located, "--print-file-name=libsynchronization.a"], WORKSPACE))
            library = Path((E / "commands/gcc-race-library/stdout").read_text(encoding="utf-8").strip())
            record["raceLibraryReported"] = str(library)
            assert library.is_absolute() and library.is_file(), "Compatible native race compiler library missing"
            record.update({"raceLibraryResolved": str(library.resolve()), "raceLibrarySHA256": sha(library.read_bytes())})
    except Exception:
        record["error"] = traceback.format_exc()
    save(E / "compiler-identity.json", record)
    assert "error" not in record, "Actual compiler prerequisite failed"


def windows_storage_preflight():
    # Match Go1.26.9 os.tempDir's native API choice, then query actual volumes.
    import ctypes
    from ctypes import wintypes
    report = {"tempEnvironment": {key: os.environ.get(key) for key in ("TMP", "TEMP", "TMPDIR")},
              "volumes": [], "errors": [],
              "goTempAPIReference": "https://github.com/golang/go/blob/go1.26.9/src/os/file_windows.go#L264"}
    try:
        kernel = ctypes.WinDLL("kernel32", use_last_error=True)
        try:
            get_temp = kernel.GetTempPath2W
            report["goNativeTempAPI"] = "GetTempPath2W"
        except AttributeError:
            get_temp = kernel.GetTempPathW
            report["goNativeTempAPI"] = "GetTempPathW"
        get_temp.argtypes = [wintypes.DWORD, wintypes.LPWSTR]
        get_temp.restype = wintypes.DWORD
        buffer = ctypes.create_unicode_buffer(32768)
        length = get_temp(len(buffer), buffer)
        assert 0 < length < len(buffer), "Native test TEMP query failed"
        native_temp = Path(buffer.value).resolve(strict=True)
        report["nativeGoTempPath"] = str(native_temp)
        configured = [Path(os.environ[key]).resolve(strict=True) for key in ("TMP", "TEMP", "TMPDIR")]
        report["nativeTempMatchesTaskScratch"] = all(value == native_temp for value in configured)
        get_root = kernel.GetVolumePathNameW
        get_root.argtypes = [wintypes.LPCWSTR, wintypes.LPWSTR, wintypes.DWORD]
        get_root.restype = wintypes.BOOL
        get_info = kernel.GetVolumeInformationW
        get_info.argtypes = [wintypes.LPCWSTR, wintypes.LPWSTR, wintypes.DWORD,
                             ctypes.POINTER(wintypes.DWORD), ctypes.POINTER(wintypes.DWORD),
                             ctypes.POINTER(wintypes.DWORD), wintypes.LPWSTR, wintypes.DWORD]
        get_info.restype = wintypes.BOOL
        paths = {"evidence": E, "actualTestTemp": native_temp,
                 "goBuildTemp": Path(os.environ["GOTMPDIR"]),
                 "candidateCheckout": WORKSPACE / "source", "baselineCheckout": WORKSPACE / "baseline",
                 "controllerCheckout": WORKSPACE / "controller"}
        for label, path in paths.items():
            record = {"role": label, "path": str(path)}
            try:
                resolved = path.resolve(strict=True)
                record["resolvedPath"] = str(resolved)
                volume, filesystem, name = (ctypes.create_unicode_buffer(32768) for _ in range(3))
                if not get_root(str(resolved), volume, len(volume)):
                    raise ctypes.WinError(ctypes.get_last_error())
                serial, maximum, flags = wintypes.DWORD(), wintypes.DWORD(), wintypes.DWORD()
                if not get_info(volume.value, name, len(name), ctypes.byref(serial),
                                ctypes.byref(maximum), ctypes.byref(flags), filesystem, len(filesystem)):
                    raise ctypes.WinError(ctypes.get_last_error())
                record.update({"volumeRoot": volume.value, "filesystem": filesystem.value,
                               "volumeName": name.value, "serial": serial.value, "flags": flags.value})
                assert filesystem.value.upper() == "NTFS", "Unknown or non-NTFS actual volume"
            except Exception:
                record["error"] = traceback.format_exc()
                report["errors"].append(record["error"])
            report["volumes"].append(record)
        assert report["nativeTempMatchesTaskScratch"], "Native Go TEMP is not assigned task scratch"
    except Exception:
        report["errors"].append(traceback.format_exc())
    save(E / "windows-storage-prerequisites.json", report)
    assert not report["errors"] and len(report["volumes"]) == 6, "Actual Windows storage prerequisite failed"


def exec_controls():
    # Only status/byte/metadata/watchdog controls, never evidence of a real Go race.
    wrapper = Path(__file__).resolve()
    controls = [("launch-failure", None, None), ("nonzero-missing-metadata", 17, None),
                ("zero-missing-metadata", 0, None), ("synthetic-marker66", 66, None),
                ("watchdog", None, 2)]
    results, errors = [], []
    for name, code, timeout in controls:
        record = {"control": name, "passed": False, "scope": "Non-project wrapper status/stream control, not Go race or business evidence"}
        try:
            stdout = ("control-" + name + "\x00\n").encode()
            stderr = b"WARNING: DATA RACE\nsynthetic wrapper status control only\n" if code == 66 else b"control-stderr\n"
            if name == "launch-failure":
                child = [str(E / "never-created-control-executable")]
                stdout, stderr = b"", b""
            else:
                script = "import sys,threading;sys.stdout.buffer.write(" + repr(stdout) + ");sys.stdout.buffer.flush();sys.stderr.buffer.write(" + repr(stderr) + ");sys.stderr.buffer.flush();"
                script += "threading.Event().wait(60)" if timeout else "sys.exit(" + str(code) + ")"
                child = [sys.executable, "-c", script]
            phase = "control-" + name
            env = dict(os.environ, G_OBSERVATION_PHASE=phase)
            outer = capture(E / "commands/wrapper-controls" / name,
                            [sys.executable, "-B", str(wrapper), "control-exec", str(timeout or 10), *child],
                            WORKSPACE, timeout=45, env=env)
            identities = list((E / "phases" / phase / "binaries").glob("*/identity.json"))
            assert len(identities) == 1
            identity = json.loads(identities[0].read_text(encoding="utf-8"))
            actual_dir = identities[0].parent / "actual"
            actual = json.loads((actual_dir / "process.json").read_text(encoding="utf-8"))
            record.update({"wrapperActual": outer, "binaryActual": actual, "metadataComplete": identity["metadataComplete"]})
            assert not identity["metadataComplete"], "Controls intentionally provide no valid Go metadata/profile"
            assert not outer["watchdog"] and outer["streamsComplete"]
            assert (E / "commands/wrapper-controls" / name / "stdout").read_bytes() == (actual_dir / "stdout").read_bytes() == stdout
            assert (E / "commands/wrapper-controls" / name / "stderr").read_bytes() == (actual_dir / "stderr").read_bytes() == stderr
            if name == "launch-failure":
                assert not actual["started"] and actual["exitCode"] is None and actual.get("launchError") and outer["exitCode"] != 0
            elif name == "watchdog":
                assert actual["started"] and actual["watchdog"] and actual["exitCode"] not in (None, 0)
                assert outer["exitCode"] == actual["exitCode"], "Watchdog actual nonzero/signal not preserved"
            else:
                assert actual["started"] and not actual["watchdog"] and actual["exitCode"] == code
                assert outer["exitCode"] == (code if code else 1), "Known status lost or actual zero metadata failure escaped"
            record["passed"] = True
        except Exception:
            record["error"] = traceback.format_exc()
            errors.append(record["error"])
        results.append(record)
    save(E / "wrapper-control-results.json", {"results": results, "errors": errors,
                                               "limitation": "Synthetic marker66 proves status/stream handling only. Actual Go race evidence still requires real project -race execution."})
    assert not errors and all(item["passed"] for item in results), "Remote wrapper controls failed; candidate execution blocked"


def profile_blocks(path, source):
    raw = path.read_bytes()
    lines = raw.decode().splitlines()
    assert lines and lines[0] == "mode: atomic" and len(lines) > 1
    blocks = {}
    for line in lines[1:]:
        match = re.fullmatch(r"(.+):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)", line)
        assert match, "Malformed actual profile block"
        name = match[1]
        assert name.startswith(MODULE + "/")
        relative = name[len(MODULE) + 1:]
        assert not Path(relative).is_absolute() and ".." not in Path(relative).parts
        file = source / "backend/third_party/req" / relative
        assert file.is_file() and not file.is_symlink() and file.suffix == ".go"
        start_line, start_col, end_line, end_col, statements, count = map(int, match.groups()[1:])
        source_lines = file.read_bytes().splitlines()
        assert 1 <= start_line <= end_line <= len(source_lines)
        assert start_col >= 1 and end_col >= 1 and statements >= 0 and count >= 0
        assert start_col <= len(source_lines[start_line-1]) + 1
        assert end_col <= len(source_lines[end_line-1]) + 1
        assert (start_line, start_col) <= (end_line, end_col)
        key = (name, start_line, start_col, end_line, end_col)
        assert key not in blocks
        blocks[key] = statements
    return blocks


def actual_inventory(phase, source, windows):
    directory = E / "phases" / phase
    events = [json.loads(line) for line in (directory / "command/stdout").read_text(encoding="utf-8").splitlines()]
    inventory = json.loads((source / "backend/third_party/req/DOWNLOAD-CONTRACTS-PROVENANCE.json").read_text(encoding="utf-8"))["expectedParentCaseInventory"]
    expected = set(inventory["linux"] + (inventory["windowsAdditional"] if windows else []))
    assert len(expected) == (33 if windows else 32)
    runs = collections.Counter((event.get("Package"), event.get("Test")) for event in events if event.get("Action") == "run")
    passes = collections.Counter((event.get("Package"), event.get("Test")) for event in events if event.get("Action") == "pass" and event.get("Test"))
    packages = objects((E / "commands/packages/stdout").read_bytes())
    test_packages = {item["ImportPath"] for item in packages if item.get("TestGoFiles") or item.get("XTestGoFiles")} if phase == "full" else {MODULE}
    bad = [event for event in events if event.get("Action") == "fail" or (event.get("Action") == "skip" and (event.get("Test") or event.get("Package") in test_packages))]
    package_passes = collections.Counter(event.get("Package") for event in events if event.get("Action") == "pass" and not event.get("Test"))
    g_events = {name: {"run": runs[(MODULE, name)], "pass": passes[(MODULE, name)]} for name in sorted(expected)}
    unexpected = sorted(name for package, name in runs if package == MODULE and name and name.startswith(("TestReqDownload", "TestReqParallelDownload")) and name not in expected and not any(other.startswith(name + "/") for other in expected))
    old = json.loads((source / "backend/tools/req-http2-security-inventory.json").read_text(encoding="utf-8"))
    original = {(old[key[:-6] + "Package"], leaf) for key, values in old.items() if key.endswith("Leaves") and isinstance(values, list) and key[:-6] + "Package" in old for leaf in values}
    assert len(original) == old["totalNamedLeaves"] == 401
    old_events = [{"package": package, "test": name, "run": runs[(package, name)], "pass": passes[(package, name)]} for package, name in sorted(original)] if phase == "full" else []
    result = {"expectedGLeaves": len(expected), "gActual": g_events, "unexpectedGLeaves": unexpected,
              "badEvents": bad, "original401Actual": old_events,
              "packageTerminals": [event for event in events if "Test" not in event and event.get("Action") in ("pass", "fail", "skip")],
              "classification": "Actual results; every failure remains failed, no accepted-RED list"}
    save(directory / "actual-inventory.json", result)
    assert not unexpected and all(value == {"run": 1, "pass": 1} for value in g_events.values())
    assert not bad, "Actual failed or skipped test-bearing package/case"
    assert all(package_passes[package] == 1 for package in test_packages), "Test-bearing package completion missing/duplicate"
    if phase == "full":
        assert all(item["run"] == item["pass"] == 1 for item in old_events)


def child_profiles(phase, source, windows):
    directory = E / "phases" / phase
    errors, records, seen = [], [], set()
    inventory = json.loads((source / "backend/third_party/req/DOWNLOAD-CONTRACTS-PROVENANCE.json").read_text(encoding="utf-8"))["expectedParentCaseInventory"]
    expected = {name for name in inventory["linux"] if name.startswith("TestReqParallelDownloadContract/")}
    if windows:
        expected.update(inventory["windowsAdditional"])
    try:
        identities = [(path.parent, json.loads(path.read_text(encoding="utf-8"))) for path in (directory / "binaries").glob("*/identity.json")]
        roots = [(path, identity) for path, identity in identities if identity["environment"].get("SUB2API_G_DOWNLOAD_PROFILE_DIR") == str(directory / "children") and (path / "actual/stdout").is_file() and b"G_CHILD_EXECUTED TestReqParallelDownloadContract/" in (path / "actual/stdout").read_bytes()]
        assert len(roots) == 1, "Actual G parent binary identity missing/duplicate"
        root, identity = roots[0]
        assert identity["beforeSHA256"] == identity["afterSHA256"]
        parent_blocks = profile_blocks(root / "parent-binary.raw.out", source)
        for path in sorted((directory / "children").glob("*/process.json")):
            record = {"metadata": str(path.relative_to(E)), "normalExitProfileCandidate": False}
            try:
                item = json.loads(path.read_text(encoding="utf-8"))
                name = item["top"] + "/" + item["scenario"]
                record.update({"case": name, "actualExit": item["exitCode"], "watchdog": item["watchdog"], "classification": item["classification"]})
                assert name in expected and name not in seen
                seen.add(name)
                output = (path.parent / "child.stdout-stderr.txt").read_bytes()
                assert item["outputBytes"] == len(output) and item["outputSHA256"] == sha(output)
                assert Path(item["arguments"][0]).resolve() == Path(identity["binary"]).resolve()
                profile = path.parent / "child.atomic.out"
                assert Path(item["profilePath"]).resolve() == profile.resolve()
                assert "-test.coverprofile=" + str(profile) in item["arguments"]
                executed = ("G_CHILD_EXECUTED " + name).encode() in output
                completed = ("G_CHILD_BODY_DONE " + name).encode() in output
                assert executed == item["executedMarker"] and completed == item["completedMarker"]
                if profile.is_file():
                    assert not profile.is_symlink()
                    raw = profile.read_bytes()
                    assert item["profileBytes"] == len(raw) and item["profileSHA256"] == sha(raw)
                    record["profileSHA256"] = sha(raw)
                    record["blockInventoryMatchesActualSameBinaryParent"] = profile_blocks(profile, source) == parent_blocks
                markers = (b"WARNING: DATA RACE", b"panic:", b"fatal error:", b"G_DOWNLOAD_ASSERT", b"G_DOWNLOAD_FILE_CLOSE_ASSERT", b"G_DOWNLOAD_COMPLETION_TIMEOUT", b"G_DOWNLOAD_UNEXPECTED_ERROR", b"G_DOWNLOAD_PRECONDITION", b"G_FIXTURE_", b"G_CHILD_WATCHDOG")
                record["normalExitProfileCandidate"] = bool(item["started"] and item["exited"] and item["exitCode"] == 0 and not item["processError"] and not item["watchdog"] and executed and completed and not item["runtimeFailureMarkers"] and not any(marker in output for marker in markers) and item["coverageMode"] == "atomic" and record.get("blockInventoryMatchesActualSameBinaryParent"))
            except Exception:
                record["integrityError"] = traceback.format_exc()
                errors.append(record["integrityError"])
            records.append(record)
        directories = {path for path in (directory / "children").iterdir()}
        declared = {path.parent for path in (directory / "children").glob("*/process.json")}
        assert directories == declared and all(path.is_dir() and not path.is_symlink() for path in directories), "Unaccounted child evidence directory"
    except Exception:
        errors.append(traceback.format_exc())
    save(directory / "actual-child-profiles.json", {"expectedChildren": len(expected), "seen": sorted(seen), "records": records, "errors": errors,
                                                   "counterPolicy": "Raw bytes only; no rewriting/merging; failed children are forensic only. Normal-exit candidate does not attest coverage PASS or waive any failed phase/job."})
    assert not errors and seen == expected and all(item["normalExitProfileCandidate"] for item in records)


def observe(kind):
    report = {"kind": kind, "source": BASE if kind == "baseline" else SOURCE, "baseline": BASE,
              "controller": os.environ["GITHUB_SHA"], "errors": [], "commands": []}
    before = {}
    roots = {"controller": (WORKSPACE / "controller", os.environ["GITHUB_SHA"]),
             "baseline": (WORKSPACE / "baseline", BASE)}
    if kind == "candidate":
        roots["candidate"] = (WORKSPACE / "source", SOURCE)
    try:
        for label, (root, head) in roots.items():
            before[label] = snapshot(root, head, label + "-before", archive=True)
        version = capture(E / "commands/go-version", ["go", "version"], WORKSPACE)
        require_success(version)
        platform = "windows/amd64" if os.name == "nt" else "linux/amd64"
        assert (E / "commands/go-version/stdout").read_text(encoding="utf-8").strip() == "go version go1.26.9 " + platform
        require_success(capture(E / "commands/go-env", ["go", "env", "-json", "GOOS", "GOARCH", "CGO_ENABLED", "CC", "GOTOOLCHAIN", "GOWORK", "GOFLAGS"], WORKSPACE))
        actual_env = json.loads((E / "commands/go-env/stdout").read_text(encoding="utf-8"))
        assert actual_env["GOOS"] + "/" + actual_env["GOARCH"] == platform
        assert actual_env["CGO_ENABLED"] == "1" and actual_env["GOTOOLCHAIN"] == "local"
        assert actual_env["GOWORK"] == "off" and actual_env["GOFLAGS"] == "-mod=readonly"
        compiler_preflight(actual_env)
        if os.name == "nt":
            windows_storage_preflight()
        if kind == "candidate":
            exec_controls()
        if kind == "baseline":
            env = dict(os.environ, REQ_SECURITY_EVIDENCE=str(E / "original401"), REQ_SECURITY_EXPECTED_HEAD=BASE)
            result = capture(E / "commands/original401", ["bash", "tools/req-http2-security-ci.sh"], WORKSPACE / "baseline/backend", timeout=3000, env=env)
            report["commands"].append(result)
            require_success(result)
        else:
            source, baseline = WORKSPACE / "source", WORKSPACE / "baseline"
            assert before["candidate"]["tree"] == TREE
            original = {item["path"]: item for item in before["baseline"]["files"]}
            current = {item["path"]: item for item in before["candidate"]["files"]}
            manifest_path = "backend/third_party/req/UPSTREAM-MANIFEST.json"
            checker_path = "backend/tools/req-http2-security-evidence.py"
            assert all(current.get(path) == item for path, item in original.items() if path not in (manifest_path, checker_path))
            original_checker = (baseline / checker_path).read_bytes()
            assert original_checker.count(b"selected.add(str(Path(relative) / name))") == 1
            assert (source / checker_path).read_bytes() == original_checker.replace(b"selected.add(str(Path(relative) / name))", b"selected.add((Path(relative) / name).as_posix())")
            assert set(current) - set(original) == {"backend/third_party/req/" + name for name in G_FILES}
            old = json.loads((baseline / manifest_path).read_text(encoding="utf-8"))
            new = json.loads((source / manifest_path).read_text(encoding="utf-8"))
            assert new["newOwnedFiles"][:len(old["newOwnedFiles"])] == old["newOwnedFiles"]
            assert set(new["newOwnedFiles"][len(old["newOwnedFiles"]):]) == G_FILES
            new["newOwnedFiles"] = old["newOwnedFiles"]
            assert new == old
            checker = source / "backend/tools/req-http2-security-evidence.py"
            preflight = E / "preflight"
            preflight.mkdir()
            require_success(capture(E / "commands/inputs", [sys.executable, "-B", str(checker), "inputs", str(source), str(preflight)], source / "backend"))
            module_graph(baseline, source)
            packages = capture(E / "commands/packages", ["go", "list", "-json", MODULE + "/..."], source / "backend")
            require_success(packages)
            shutil.copyfile(E / "commands/candidate-modules/stdout", preflight / "modules.json")
            shutil.copyfile(E / "commands/packages/stdout", preflight / "packages.json")
            require_success(capture(E / "commands/existing-packages-checker", [sys.executable, "-B", str(checker), "packages", str(source / "backend"), str(preflight)], source / "backend"))
            selector = "^(TestReqDownloadContract|TestReqDownloadContractCancellation|TestReqParallelDownloadContract"
            selector += "|TestReqParallelDownloadWindowsClose)$" if os.name == "nt" else ")$"
            wrapper = Path(__file__).resolve()
            # cmd/go's quoted.Split accepts quoted argv items; no shell expansion.
            assert '"' not in str(wrapper) and '"' not in sys.executable
            exec_flag = '"' + sys.executable + '" "' + str(wrapper) + '" exec'
            for phase in ("named", "full"):
                phase_dir = E / "phases" / phase
                (phase_dir / "children").mkdir(parents=True)
                env = dict(os.environ, G_OBSERVATION_PHASE=phase, SUB2API_G_DOWNLOAD_PROFILE_DIR=str(phase_dir / "children"))
                argv = ["go", "test", "-p=1", "-parallel=1", "-race", "-count=1", "-timeout=15m", "-json", "-covermode=atomic", "-coverpkg=" + MODULE + "/...", "-coverprofile=" + str(phase_dir / "parent.raw.out"), "-exec=" + exec_flag]
                if phase == "named":
                    argv += ["-run=" + selector, MODULE]
                else:
                    argv += [MODULE + "/..."]
                result = capture(phase_dir / "command", argv, source / "backend", timeout=2000 if phase == "named" else 3000, env=env)
                report["commands"].append(result)
                for label, check in (("actual-command", lambda: require_success(result)),
                                     ("inventory", lambda: actual_inventory(phase, source, os.name == "nt")),
                                     ("raw-parent-profile", lambda: profile_blocks(phase_dir / "parent.raw.out", source)),
                                     ("child-profiles", lambda: child_profiles(phase, source, os.name == "nt"))):
                    try:
                        check()
                    except Exception:
                        report["errors"].append({"phase": phase, "check": label, "error": traceback.format_exc()})
    except Exception:
        report["errors"].append({"check": "prerequisite-or-observation", "error": traceback.format_exc()})
    finally:
        for label, (root, head) in roots.items():
            try:
                after = snapshot(root, head, label + "-after")
                assert before[label] == after
            except Exception:
                report["errors"].append({"check": label + "-after-freeze", "error": traceback.format_exc()})
        report["policy"] = "Every actual failure fails this job; no accepted-RED list or profile counter merge. Baseline401 and platform jobs remain independent obligations."
        save(E / "actual-observation.json", report)
    return 1 if report["errors"] else 0


if __name__ == "__main__":
    if sys.argv[1] == "exec":
        sys.exit(exec_mode(sys.argv[2:]))
    if sys.argv[1] == "control-exec":
        sys.exit(exec_mode(sys.argv[3:], timeout=float(sys.argv[2])))
    sys.exit(observe(sys.argv[1]))
