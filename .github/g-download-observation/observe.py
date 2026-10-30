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

BASE = "673c3fafa9ff9c4c32a6adbd8cd5f595c25f0c63"
# Frozen reviewed source plus the retained eight-path observation infrastructure.
# OLD trees are direct test-only children with identical final NEW fixtures.
# Runtime acceptance still requires actual evidence; these pins are not PASS.
# Exact formatted880 production plus retained observer infrastructure. Ready
# means frozen for remote execution, not that any new test or gate has passed.
SOURCE_READY = False
SOURCE = "b44e3163c53e19e1f3e9b46c734a341577c4046d"
TREE = "56820e3a466c97f6fbc308dd26ac68e3b016a148"
PRODUCTION = "a6b5efd158dfbd299a9a38b9ecd7707ec974a52a"
OLD_PRODUCTION = "bf4dfe25dac427186ff32c93a5c9d45d89530418"
OLD_SOURCE = "79d98c871e16fc982a40b82f39755fd9439ac051"
OLD_TREE = "a214bc81ba0511b1ac69a8c2d136ba82b829b84b"
OLD_FIXTURES = ("dump_ownership_contract_test.go", "dump_ownership_peer_test.go")
OLD_TLS_PRODUCTION = "e1b71933de5e6d0c1124576ce98d5594a5c3d050"
OLD_TLS_SOURCE = "2768a561c693ebcb194357317183aafdb5741b64"
OLD_TLS_TREE = "bcfac4d7925e643f7296af24892c0ee6e0979792"
OLD_TLS_FIXTURES = ("custom_tls_handshake_peer_test.go", "custom_tls_handshake_test.go",
                    "custom_tls_handshake_boundary_test.go", "internal/http2/custom_tls_handshake_peer_test.go",
                    "internal/http2/custom_tls_handshake_test.go")
INFRA_PATHS = {".github/g-download-observation/observe.py", ".github/g-download-observation/controls.py",
               ".github/g-download-observation/metadata_control_test.go.txt", ".github/workflows/g-download-observe.yml",
               "backend/tools/req-http2-security-evidence.py", "backend/third_party/req/download_wire_peer_test.go",
               "backend/third_party/req/DOWNLOAD-CONTRACTS-PROVENANCE.json", "backend/third_party/req/DOWNLOAD-CONTRACTS-REVIEW.md"}
MODULE = "github.com/imroc/req/v3"
EXPANSION_GROUPS = (("publicAPI", MODULE, 70), ("http1Boundary", MODULE, 33),
                    ("publicDefault", MODULE, 6), ("bisectContract", MODULE + "/internal/bisect", 112),
                    ("http2Coverage", MODULE + "/internal/http2", 51), ("safariWrapper", MODULE, 3))
EXPANSION_PREFIXES = ("TestReqPublicAPI_", "TestReqH1Boundary_", "TestReqPublicDefaultContract",
                      "TestReqBisectContract_", "TestReqHTTP2CoverageContract_",
                      "TestReqHTTP2CoverageFrame_", "TestReqSafariWrapperContract")
LEGACY605_IDENTITY_SHA256 = "d40da89d1c9592dea18a8457de03b430eb715f633f0aa6215bacb1b7c26be8c8"
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


def unique_object(pairs):
    result = {}
    for name, value in pairs:
        assert name not in result, "Duplicate metadata JSON field"
        result[name] = value
    return result


def seal_aux_metadata(coverdir, destination):
    """Archive only actual cmd/go auxiliary inputs, never runtime counters."""
    destination.mkdir(parents=True, exist_ok=False)
    collection = coverdir / "metafiles.txt"
    assert collection.is_file() and not collection.is_symlink()
    raw = collection.read_bytes()
    data = json.loads(raw, object_pairs_hook=unique_object)
    assert set(data) == {"ImportPaths", "MetaFileFragments"}
    imports, paths = data["ImportPaths"], data["MetaFileFragments"]
    assert isinstance(imports, list) and isinstance(paths, list) and len(imports) == len(paths) > 0
    assert all(isinstance(value, str) and value for value in imports + paths)
    assert len(set(imports)) == len(imports) and len(set(paths)) == len(paths)
    (destination / "metafiles.txt").write_bytes(raw)
    fragments = []
    for index, (package, name) in enumerate(zip(imports, paths)):
        path = Path(name)
        assert path.is_absolute() and path.is_file() and not path.is_symlink()
        value = path.read_bytes()
        assert value, "Empty auxiliary coverage fragment"
        (destination / (str(index) + ".covmeta")).write_bytes(value)
        fragments.append({"ImportPath": package, "Path": name, "Bytes": len(value), "SHA256": sha(value)})
    record = {"coverdir": str(coverdir), "MetadataSHA256": sha(raw), "Fragments": fragments}
    save(destination / "seal.json", record)
    return record


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
    ignored = git(root, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
    (E / (label + ".ignored")).write_bytes(ignored)
    assert not ignored, "Ignored generated/residual input outside frozen checkout: " + label
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
                "argv": argv, "phase": phase, "source": os.environ.get("G_OBSERVATION_SOURCE_HEAD", SOURCE),
                "sourceScope": "Owned synthetic metadata control module projected from exact source helper" if phase == "metadata-controls" else "Actual frozen application test binary",
                "actualWorkingDirectory": str(Path.cwd()),
                "environment": {key: os.environ.get(key) for key in
                                ("GOOS", "GOARCH", "CGO_ENABLED", "GOTOOLCHAIN", "GOWORK",
                                 "GOFLAGS", "GOMAXPROCS", "GORACE", "GOCOVERDIR", "SUB2API_G_DOWNLOAD_PROFILE_DIR", "TMP", "TEMP", "TMPDIR", "GOTMPDIR")}}
    try:
        if phase in ("old-calibration", "old-resource", "old-tls-race"):
            identity["sourceGitBefore"] = git(Path.cwd(), "rev-parse", "HEAD", "HEAD^{tree}").decode().splitlines()
        before = binary.read_bytes()
        identity.update({"beforeSHA256": sha(before), "bytes": len(before)})
        if phase in ("old-calibration", "old-resource", "old-tls-race"):
            (directory / "actual-binary.exe").write_bytes(before)
    except OSError as error:
        identity["binaryReadError"] = repr(error)
    save(directory / "identity.json", identity)
    aux_before = None
    try:
        coverdirs = [value.split("=", 1)[1] for value in argv[1:] if value.startswith("-test.gocoverdir=")]
        assert len(coverdirs) == 1 and Path(coverdirs[0]).is_absolute(), "Actual parent coverage directory missing/duplicate"
        aux_before = seal_aux_metadata(Path(coverdirs[0]), directory / "aux-before")
        identity["auxBefore"] = aux_before
    except Exception:
        identity["auxBeforeError"] = traceback.format_exc()
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
        if phase in ("old-calibration", "old-resource", "old-tls-race"):
            identity["sourceGitAfter"] = git(Path.cwd(), "rev-parse", "HEAD", "HEAD^{tree}").decode().splitlines()
            assert identity["sourceGitBefore"] == identity["sourceGitAfter"] == [os.environ["G_OBSERVATION_SOURCE_HEAD"], os.environ["G_OBSERVATION_SOURCE_TREE"]]
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
        assert aux_before is not None, "Complete original auxiliary metadata was not sealed"
        aux_after = seal_aux_metadata(Path(aux_before["coverdir"]), directory / "aux-after")
        assert aux_before == aux_after, "Actual cmd/go auxiliary metadata changed during test execution"
        identity["auxAfter"] = aux_after
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
    # Match pinned Go's native os.tempDir API choice, then query actual volumes.
    import ctypes
    from ctypes import wintypes
    report = {"tempEnvironment": {key: os.environ.get(key) for key in ("TMP", "TEMP", "TMPDIR")},
              "volumes": [], "errors": [],
              "goTempAPIReference": "https://github.com/golang/go/blob/go1.26.10/src/os/file_windows.go"}
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
        if (WORKSPACE / "old").is_dir():
            paths["oldCheckout"] = WORKSPACE / "old"
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
    assert not report["errors"] and len(report["volumes"]) == len(paths), "Actual Windows storage prerequisite failed"


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


def profile_blocks(path, source, aggregate=False, module=MODULE, module_root=None):
    raw = path.read_bytes()
    lines = raw.decode().splitlines()
    assert lines and lines[0] == "mode: atomic" and len(lines) > 1
    blocks = {}
    for line in lines[1:]:
        match = re.fullmatch(r"(.+):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)", line)
        assert match, "Malformed actual profile block"
        name = match[1]
        assert name.startswith(module + "/")
        relative = name[len(module) + 1:]
        assert not Path(relative).is_absolute() and ".." not in Path(relative).parts
        file = (module_root if module_root is not None else source / "backend/third_party/req") / relative
        assert file.is_file() and not file.is_symlink() and file.suffix == ".go"
        start_line, start_col, end_line, end_col, statements, count = map(int, match.groups()[1:])
        source_lines = file.read_bytes().splitlines()
        assert 1 <= start_line <= end_line <= len(source_lines)
        assert start_col >= 1 and end_col >= 1 and statements >= 0 and count >= 0
        assert start_col <= len(source_lines[start_line-1]) + 1
        assert end_col <= len(source_lines[end_line-1]) + 1
        assert (start_line, start_col) <= (end_line, end_col)
        key = (name, start_line, start_col, end_line, end_col)
        if key in blocks:
            assert aggregate and blocks[key] == statements, "Repeated profile definition has incompatible statements or context"
        blocks[key] = statements
    if aggregate:
        save(Path(str(path) + ".definitions.json"), {"rawSHA256": sha(raw), "rawRows": len(lines) - 1,
             "distinctDefinitions": len(blocks), "repeatedRows": len(lines) - 1 - len(blocks),
             "policy": "All original rows/counts remain untouched. This map compares definitions only; no counter addition or child merge."})
    return blocks


def actual_inventory(phase, source, windows):
    directory = E / "phases" / phase
    events = [json.loads(line) for line in (directory / "command/stdout").read_text(encoding="utf-8").splitlines()]
    inventory = json.loads((source / "backend/third_party/req/DOWNLOAD-CONTRACTS-PROVENANCE.json").read_text(encoding="utf-8"))["expectedParentCaseInventory"]
    selected_inventory = json.loads((source / "backend/tools/req-http2-security-inventory.json").read_text(encoding="utf-8"))
    assert set(inventory["linux"]) == set(selected_inventory["downloadContractLeaves"]) and len(inventory["linux"]) == 32
    fallback = selected_inventory["parallelDownloadFallbackLeaves"]
    cleanup = selected_inventory["parallelDownloadHTTP3CleanupLeaves"]
    in_process = selected_inventory["parallelDownloadInProcessLeaves"]
    default_output = selected_inventory["parallelDownloadDefaultOutputLeaves"]
    ownership = selected_inventory["dumpOwnershipLeaves"]
    h2_receive = selected_inventory["http2ReceiveFlowLeaves"]
    tls_root = selected_inventory["customTLSLeaves"]
    tls_h2 = selected_inventory["customTLSHTTP2Leaves"]
    h2_package = MODULE + "/internal/http2"
    assert selected_inventory["dumpOwnershipPackage"] == MODULE
    assert len(ownership) == len(set(ownership)) == selected_inventory["dumpOwnershipCount"] == 42
    assert selected_inventory["http2ReceiveFlowPackage"] == h2_package
    assert len(h2_receive) == len(set(h2_receive)) == selected_inventory["http2ReceiveFlowCount"] == 6
    assert selected_inventory["customTLSPackage"] == MODULE
    assert len(tls_root) == len(set(tls_root)) == selected_inventory["customTLSCount"] == 18
    assert selected_inventory["customTLSHTTP2Package"] == h2_package
    assert len(tls_h2) == len(set(tls_h2)) == selected_inventory["customTLSHTTP2Count"] == 13
    manifest = json.loads((source / "backend/third_party/req/UPSTREAM-MANIFEST.json").read_text(encoding="utf-8"))
    assert ownership == manifest["ownedDumpClose1752"]["requiredNewTestLeaves"]
    assert h2_receive == manifest["http2ReceiveFlowContracts"]["leaves"]
    tls_contract = manifest["customTLSOwnershipContracts"]
    assert tls_root == tls_contract["rootLeaves"] and tls_contract["rootPackage"] == MODULE and tls_contract["rootCount"] == 18
    assert tls_h2 == tls_contract["privateHTTP2Leaves"] and tls_contract["privateHTTP2Package"] == h2_package and tls_contract["privateHTTP2Count"] == 13
    assert tls_contract["priorMandatoryLeaves"] == 574 and tls_contract["composedMandatoryLeaves"] == 605
    assert len(fallback) == len(cleanup) == 1
    assert selected_inventory["parallelDownloadInProcessPackage"] == MODULE
    assert len(in_process) == len(set(in_process)) == selected_inventory["parallelDownloadInProcessCount"] == 23
    isolated_parallel = [name for name in inventory["linux"] if name.startswith("TestReqParallelDownloadContract/")]
    assert len(isolated_parallel) == 23
    assert in_process == [name.replace("TestReqParallelDownloadContract/", "TestReqParallelDownloadInProcessContract/", 1) for name in isolated_parallel]
    provenance = json.loads((source / "backend/third_party/req/DOWNLOAD-CONTRACTS-PROVENANCE.json").read_text(encoding="utf-8"))
    assert provenance["inProcessContracts"]["leaves"] == in_process and provenance["inProcessContracts"]["count"] == 23
    assert selected_inventory["parallelDownloadDefaultOutputPackage"] == MODULE
    assert selected_inventory["parallelDownloadDefaultOutputCount"] == len(default_output) == 1
    assert default_output == ["TestReqParallelDownloadDefaultOutput/url_path_filename"]
    assert provenance["defaultOutputContract"]["leaves"] == default_output
    expected = set(inventory["linux"] + fallback + cleanup + in_process + default_output + ownership + tls_root + (inventory["windowsAdditional"] if windows else []))
    assert len(expected) == (119 if windows else 118)
    expansion = set()
    for kind, package, count in EXPANSION_GROUPS:
        leaves = selected_inventory[kind + "Leaves"]
        assert selected_inventory[kind + "Package"] == package
        assert len(leaves) == len(set(leaves)) == selected_inventory[kind + "Count"] == count
        pairs = {(package, name) for name in leaves}
        assert not expansion & pairs
        expansion.update(pairs)
    assert len(expansion) == 275
    expected.update(name for package, name in expansion if package == MODULE)
    assert len(expected) == (231 if windows else 230)
    h2_expected = set(h2_receive + tls_h2) | {name for package, name in expansion if package == h2_package}
    assert len(h2_expected) == 70
    bisect_package = MODULE + "/internal/bisect"
    bisect_expected = {name for package, name in expansion if package == bisect_package}
    assert len(bisect_expected) == 112
    runs = collections.Counter((event.get("Package"), event.get("Test")) for event in events if event.get("Action") == "run")
    passes = collections.Counter((event.get("Package"), event.get("Test")) for event in events if event.get("Action") == "pass" and event.get("Test"))
    packages = objects((E / "commands/packages/stdout").read_bytes())
    test_packages = {item["ImportPath"] for item in packages if item.get("TestGoFiles") or item.get("XTestGoFiles")} if phase == "full" else {MODULE, h2_package, bisect_package}
    bad = [event for event in events if event.get("Action") == "fail" or (event.get("Action") == "skip" and (event.get("Test") or event.get("Package") in test_packages))]
    package_passes = collections.Counter(event.get("Package") for event in events if event.get("Action") == "pass" and not event.get("Test"))
    g_events = {name: {"run": runs[(MODULE, name)], "pass": passes[(MODULE, name)]} for name in sorted(expected)}
    h2_events = {name: {"run": runs[(h2_package, name)], "pass": passes[(h2_package, name)]} for name in sorted(h2_expected)}
    bisect_events = {name: {"run": runs[(bisect_package, name)], "pass": passes[(bisect_package, name)]} for name in sorted(bisect_expected)}
    expansion_events = [{"package": package, "test": name, "run": runs[(package, name)], "pass": passes[(package, name)]} for package, name in sorted(expansion)]
    expansion_runs = {key for key in runs if key[1] and key[1].startswith(EXPANSION_PREFIXES)}
    expansion_actual_leaves = {key for key in expansion_runs if not any(other[0] == key[0] and other[1].startswith(key[1] + "/") for other in expansion_runs)}
    assert expansion_actual_leaves == expansion, "Unexpected/missing expansion leaf, including wrong-package execution"
    # Two writer-drain controls deliberately own no file; every other ownership
    # leaf must witness an actual successful Remove, not just absence of Fatal.
    file_ownership = set(ownership) - {"TestReqDumpOwnershipCloseDrains/async_false", "TestReqDumpOwnershipCloseDrains/async_true"}
    assert len(file_ownership) == 40
    remove_successes = {name: sum(event.get("Output", "").count("DUMP1752_RESOURCE_REMOVE_OK operation=remove")
                                 for event in events if event.get("Package") == MODULE and event.get("Test") == name)
                        for name in sorted(file_ownership)}
    parent_execution = []
    for name in in_process:
        output = "".join(event.get("Output", "") for event in events if event.get("Package") == MODULE and event.get("Test") == name)
        parent_execution.append({"test": name,
                                 "executedMarkers": output.count("G_IN_PROCESS_EXECUTED " + name + "\n"),
                                 "completedMarkers": output.count("G_IN_PROCESS_BODY_DONE " + name + "\n"),
                                 "childDelegationMarkers": "G_CHILD_ACTUAL" in output or "G_CHILD_EXECUTED" in output})
    default_execution = []
    for name in default_output:
        output = "".join(event.get("Output", "") for event in events if event.get("Package") == MODULE and event.get("Test") == name)
        default_execution.append({"test": name,
                                  "completedMarkers": output.count("G1748_DEFAULT_OUTPUT_COMPLETE " + name + " "),
                                  "childDelegationMarkers": "G_CHILD_ACTUAL" in output or "G_CHILD_EXECUTED" in output})
    unexpected = sorted(name for package, name in runs if package == MODULE and name and name.startswith(("TestReqDownload", "TestReqParallelDownload", "TestReqDumpOwnership", "TestReqCustomTLS_")) and name not in expected and not any(other.startswith(name + "/") for other in expected))
    unexpected_h2 = sorted(name for package, name in runs if package == h2_package and name and name.startswith(("TestReqHTTP2ReceiveFlowContract_", "TestReqCustomTLS_")) and name not in h2_expected and not any(other.startswith(name + "/") for other in h2_expected))
    old = selected_inventory
    registered = {(old[key[:-6] + "Package"], leaf) for key, values in old.items() if key.endswith("Leaves") and isinstance(values, list) and key[:-6] + "Package" in old for leaf in values}
    assert len(registered) == old["totalNamedLeaves"] == 880 and expansion < registered
    original = registered - expansion
    assert len(original) == 605
    assert sha(json.dumps(sorted(original), separators=(",", ":")).encode()) == LEGACY605_IDENTITY_SHA256
    previous574 = original - {(MODULE, name) for name in tls_root} - {(h2_package, name) for name in tls_h2}
    assert len(previous574) == 574
    previous526 = previous574 - {(MODULE, name) for name in ownership} - {(h2_package, name) for name in h2_receive}
    assert len(previous526) == 526
    previous525 = previous526 - {(MODULE, name) for name in default_output}
    assert len(previous525) == 525
    previous502 = previous525 - {(MODULE, name) for name in in_process}
    assert len(previous502) == 502
    baseline_inventory = json.loads((WORKSPACE / "baseline/backend/tools/req-http2-security-inventory.json").read_text(encoding="utf-8"))
    retained = {(baseline_inventory[key[:-6] + "Package"], leaf) for key, values in baseline_inventory.items() if key.endswith("Leaves") and isinstance(values, list) and key[:-6] + "Package" in baseline_inventory for leaf in values}
    assert len(retained) == baseline_inventory["totalNamedLeaves"] == 468 and retained < previous502 < previous526 < original
    old_events = [{"package": package, "test": name, "run": runs[(package, name)], "pass": passes[(package, name)]} for package, name in sorted(original)] if phase == "full" else []
    registered_events = [{"package": package, "test": name, "run": runs[(package, name)], "pass": passes[(package, name)]} for package, name in sorted(registered)] if phase == "full" else []
    previous_events = [item for item in old_events if (item["package"], item["test"]) in previous502]
    previous525_events = [item for item in old_events if (item["package"], item["test"]) in previous525]
    adapted_original = []
    trace_completions = []
    file_identity_markers = 0
    if phase == "full":
        required_originals = {"request_test.go": ["TestSetFile", "TestTraceInfo", "TestEnableDumpToFIle"],
                              "client_test.go": ["TestEnableDumpAllToFile"]}
        assert len(manifest["originalTestAdaptations"]) == 2
        assert {item["path"]: item["requiredTests"] for item in manifest["originalTestAdaptations"]} == required_originals
        for name in ("TestSetFile", "TestTraceInfo", "TestEnableDumpToFIle", "TestEnableDumpAllToFile"):
            selected = [event for event in events if event.get("Package") == MODULE and event.get("Test") == name]
            output = "".join(event.get("Output", "") for event in selected)
            adapted_original.append({"test": name, "run": runs[(MODULE, name)], "pass": passes[(MODULE, name)],
                                     "failedOrSkipped": any(event.get("Action") in ("fail", "skip") for event in selected)})
            if name == "TestTraceInfo":
                trace_completions = re.findall(r'TRACE_ACTUAL_COMPLETION enabled=(true|false) expectedHTTP=([12]) expectedReuse=(true|false) actualALPN="([^"]*)"', output)
            elif name == "TestSetFile":
                file_identity_markers = output.count("FILE_ACTUAL_MISSING_IDENTITY isNotExist=true ")
    result = {"expectedGLeaves": len(expected), "gActual": g_events, "unexpectedGLeaves": unexpected,
              "badEvents": bad, "mandatory605Actual": old_events,
              "registered880Actual": registered_events, "expansion275Actual": expansion_events,
              "bisectActual": bisect_events, "focusedNamedExpectedLeaves": len(expected) + len(h2_expected) + len(bisect_expected),
              "mandatory574Actual": [item for item in old_events if (item["package"], item["test"]) in previous574],
              "customTLSRootActual": {name: g_events[name] for name in tls_root},
              "customTLSHTTP2Actual": {name: h2_events[name] for name in tls_h2},
              "mandatory526Actual": [item for item in old_events if (item["package"], item["test"]) in previous526],
              "mandatory525Actual": previous525_events,
              "h2ReceiveActual": h2_events, "unexpectedH2Leaves": unexpected_h2,
              "actualRemoveSuccesses": remove_successes,
              "mandatory502Actual": previous_events, "defaultOutputActual": default_execution,
              "inProcess23Actual": parent_execution, "retainedBaselineLeaves": 468,
              "adaptedOriginalTests": adapted_original, "actualTraceCompletions": trace_completions,
              "actualMissingFileIdentityMarkers": file_identity_markers,
              "packageTerminals": [event for event in events if "Test" not in event and event.get("Action") in ("pass", "fail", "skip")],
              "classification": "Actual results; every failure remains failed, no accepted-RED list"}
    save(directory / "actual-inventory.json", result)
    assert not unexpected and not unexpected_h2 and all(value == {"run": 1, "pass": 1} for value in g_events.values())
    assert all(value == {"run": 1, "pass": 1} for value in h2_events.values())
    assert all(value == {"run": 1, "pass": 1} for value in bisect_events.values())
    assert all(item["run"] == item["pass"] == 1 for item in expansion_events)
    for name in selected_inventory["safariWrapperLeaves"]:
        output = "".join(event.get("Output", "") for event in events if event.get("Package") == MODULE and event.get("Test") == name)
        assert output.count("SAFARI_WRAPPER_WIRE_OBSERVED ") == 1
        assert output.count("SAFARI_WRAPPER_PEER_COMPLETE hellos=1 exchanges=1") == 1
        assert "SAFARI_WRAPPER_FIXTURE_FAILURE:" not in output
    assert all(count >= 1 for count in remove_successes.values()), "Actual successful ownership Remove witness missing"
    assert all(item["executedMarkers"] == item["completedMarkers"] == 1 and not item["childDelegationMarkers"] for item in parent_execution), "Missing/duplicate parent execution markers or incorrect child delegation"
    assert all(item["completedMarkers"] == 1 and not item["childDelegationMarkers"] for item in default_execution), "Default output completion missing/duplicate or incorrectly delegated to child"
    assert not bad, "Actual failed or skipped test-bearing package/case"
    assert all(package_passes[package] == 1 for package in test_packages), "Test-bearing package completion missing/duplicate"
    if phase == "full":
        assert len(registered_events) == 880 and all(item["run"] == item["pass"] == 1 for item in registered_events)
        assert all(item["run"] == item["pass"] == 1 for item in old_events)
        assert len(adapted_original) == 4 and all(item["run"] == item["pass"] == 1 and not item["failedOrSkipped"] for item in adapted_original)
        expected_trace = collections.Counter((enabled, protocol, reuse) for protocol in ("1", "2")
                                             for enabled, reuse in (("true", "false"), ("false", "true"), ("true", "true")))
        assert collections.Counter(item[:3] for item in trace_completions) == expected_trace
        assert all(alpn == "h2" if protocol == "2" else alpn in ("", "http/1.1")
                   for _, protocol, _, alpn in trace_completions)
        assert file_identity_markers == 1


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
                    aux = json.loads((path.parent / "aux-metadata.json").read_text(encoding="utf-8"), object_pairs_hook=unique_object)
                    child_coverdir = path.parent / "covdata"
                    assert Path(aux["ChildCoverDir"]).resolve() == child_coverdir.resolve()
                    assert "-test.gocoverdir=" + str(child_coverdir) in item["arguments"]
                    assert item["goCoverDirEnvironment"] == [str(child_coverdir)]
                    assert aux["MetadataSHA256"] == identity["auxBefore"]["MetadataSHA256"]
                    assert aux["Fragments"] == identity["auxBefore"]["Fragments"]
                    assert (child_coverdir / "metafiles.txt").read_bytes() == (root / "aux-before/metafiles.txt").read_bytes()
                    assert Path(aux["ParentCoverDir"]).resolve() == Path(identity["auxBefore"]["coverdir"]).resolve()
                    record["auxiliaryMetadataMatchesSameBinaryParent"] = True
                markers = (b"WARNING: DATA RACE", b"panic:", b"fatal error:", b"G_DOWNLOAD_ASSERT", b"G_DOWNLOAD_FILE_CLOSE_ASSERT", b"G_DOWNLOAD_COMPLETION_TIMEOUT", b"G_DOWNLOAD_UNEXPECTED_ERROR", b"G_DOWNLOAD_PRECONDITION", b"G_FIXTURE_", b"G_CHILD_WATCHDOG")
                record["normalExitProfileCandidate"] = bool(item["started"] and item["exited"] and item["exitCode"] == 0 and not item["processError"] and not item["watchdog"] and executed and completed and not item["runtimeFailureMarkers"] and not any(marker in output for marker in markers) and item["coverageMode"] == "atomic" and record.get("blockInventoryMatchesActualSameBinaryParent") and record.get("auxiliaryMetadataMatchesSameBinaryParent"))
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


def old_test_only_inputs(old, source, frozen):
    """OLD is an immutable Git child, never a runner rewrite of production."""
    assert git(old, "rev-parse", "HEAD^{tree}").decode().strip() == OLD_TREE
    parents = git(old, "show", "-s", "--format=%P", "HEAD").decode().split()
    assert parents == [OLD_PRODUCTION], "OLD must be a direct test-only child of preserved bf4"
    anchor = {}
    for row in git(old, "ls-tree", "--full-tree", "-r", "-z", OLD_PRODUCTION).split(b"\0"):
        if row:
            header, name = row.split(b"\t", 1)
            mode, kind, blob = header.decode().split()
            assert kind == "blob"
            anchor[os.fsdecode(name)] = (mode, blob)
    current = {item["path"]: item for item in frozen["files"]}
    paths = {"backend/third_party/req/" + name for name in OLD_FIXTURES}
    manifest_path = "backend/third_party/req/UPSTREAM-MANIFEST.json"
    assert not set(anchor) - set(current)
    assert set(current) - set(anchor) == paths, "OLD may add only the two identical fixtures"
    assert all((current[name]["mode"], current[name]["blob"]) == value
               for name, value in anchor.items() if name != manifest_path), "OLD production/original tests changed"
    assert current[manifest_path]["mode"] == anchor[manifest_path][0], "OLD manifest mode changed"
    seals = {}
    for name in OLD_FIXTURES:
        relative = "backend/third_party/req/" + name
        raw = (old / relative).read_bytes()
        assert raw == (source / relative).read_bytes(), "OLD/NEW ownership fixture bytes differ"
        seals[name] = sha(raw)
    peer = (source / "backend/third_party/req/dump_ownership_peer_test.go").read_bytes()
    assert b"DUMP1752_RESOURCE_REMOVE_OK operation=remove" in peer
    assert b"DUMP1752_RESOURCE_REMOVE_ERR type=%T operation=%s errno=%d" in peer
    original = json.loads(git(old, "show", OLD_PRODUCTION + ":" + manifest_path))
    actual = json.loads((old / manifest_path).read_text(encoding="utf-8"))
    assert set(actual) == set(original) | {"ownedDumpWitnessControl"}
    assert all(actual[name] == value for name, value in original.items() if name != "newOwnedFiles")
    assert actual["newOwnedFiles"] == original["newOwnedFiles"] + list(OLD_FIXTURES)
    assert actual["ownedDumpWitnessControl"] == {
        "productionHead": OLD_PRODUCTION, "fixtureSHA256": seals,
        "scope": "test-only Windows resource witness; no CloseDump implementation"}
    save(E / "old-test-only-inputs.json", {"oldHead": OLD_SOURCE, "oldTree": OLD_TREE,
         "productionHead": OLD_PRODUCTION, "fixtureSHA256": seals,
         "policy": "All other tracked modes/blobs match bf4; original132 and13/1/118 partition unchanged"})


def old_event_ledger(events, phase):
    """Every actual OLD JSON event belongs to one complete, ordered ledger."""
    assert phase in ("old-calibration", "old-resource", "old-tls-race")
    calibration = phase == "old-calibration"
    parent = "TestReqDumpOwnershipWindowsCalibration" if calibration else "TestReqDumpOwnershipFileLifecycle"
    expected = {parent} if calibration else {parent, parent + "/request_sync", parent + "/client_sync"}
    if phase == "old-tls-race":
        parent = "TestReqCustomTLS_LatePublicationRace"
        expected = {parent, parent + "/HTTP1", parent + "/HTTP2"}
    terminal = "pass" if calibration else "fail"
    states = {name: "pending" for name in expected}
    package_state = "pending"
    for event in events:
        assert isinstance(event, dict) and event.get("Package") == MODULE, "Unexpected OLD event package"
        action = event.get("Action")
        assert package_state != "done", "OLD event after package terminal"
        if "Test" not in event:
            if action == "start":
                assert package_state == "pending"
                package_state = "running"
            elif action == "output":
                assert package_state == "running" and isinstance(event.get("Output"), str)
            else:
                assert action == terminal and package_state == "running"
                assert all(state == "done" for state in states.values()), "Incomplete OLD test ledger"
                package_state = "done"
            continue
        name = event["Test"]
        assert name in states and package_state == "running", "Unexpected OLD test identity"
        if action == "run":
            assert states[name] == "pending", "Duplicate OLD test RUN"
            assert name == parent or states[parent] == "running", "OLD child outside running parent"
            states[name] = "running"
        elif action == "output":
            assert states[name] == "running" and isinstance(event.get("Output"), str), "OLD output outside RUN/terminal"
        else:
            assert action == terminal and states[name] == "running", "Illegal/duplicate OLD test terminal"
            assert name != parent or all(states[child] == "done" for child in expected - {parent}), "OLD parent completed before children"
            states[name] = "done"
    assert package_state == "done" and all(state == "done" for state in states.values()), "OLD ledger incomplete"
    return {"package": MODULE, "terminal": terminal, "tests": sorted(expected), "orderedComplete": True}


def old_binary_evidence(directory, phase, selector, old, actual_exit, head=OLD_SOURCE, tree=OLD_TREE):
    """A real failing binary still needs complete immutable metadata/profile proof."""
    binaries = directory / "binaries"
    entries = list(binaries.iterdir())
    assert len(entries) == 1 and entries[0].is_dir() and not entries[0].is_symlink(), "OLD binary identity missing/duplicate"
    root = entries[0]
    identity = json.loads((root / "identity.json").read_text(encoding="utf-8"))
    assert identity["phase"] == phase and identity["source"] == head
    assert identity["sourceGitBefore"] == identity["sourceGitAfter"] == [head, tree]
    assert Path(identity["actualWorkingDirectory"]).resolve() == (old / "backend/third_party/req").resolve()
    assert identity["metadataComplete"] is True
    assert not any(key.endswith("Error") for key in identity), "OLD binary metadata contains an error"
    assert re.fullmatch(r"[0-9a-f]{64}", identity["beforeSHA256"])
    assert identity["beforeSHA256"] == identity["afterSHA256"] and identity["bytes"] > 0
    binary_raw = (root / "actual-binary.exe").read_bytes()
    assert len(binary_raw) == identity["bytes"] and sha(binary_raw) == identity["beforeSHA256"]
    argv = identity["argv"]
    assert argv and Path(argv[0]).is_absolute() and str(Path(argv[0]).resolve()) == identity["binary"]
    for flag in ("-test.run=" + selector, "-test.count=1", "-test.parallel=1", "-test.timeout=3m0s", "-test.v=test2json"):
        assert argv.count(flag) == 1, "OLD binary argv differs"
    for prefix in ("-test.run=", "-test.count=", "-test.parallel=", "-test.timeout=", "-test.v=", "-test.coverprofile=", "-test.gocoverdir="):
        assert sum(arg.startswith(prefix) for arg in argv[1:]) == 1, "OLD binary argv missing/duplicate flag"
    for key in ("GOOS", "GOARCH", "CGO_ENABLED", "GOTOOLCHAIN", "GOWORK", "GOFLAGS", "GOMAXPROCS", "GORACE", "TMP", "TEMP", "TMPDIR", "GOTMPDIR"):
        assert identity["environment"][key] == os.environ.get(key), "OLD binary environment differs"
    actual_dir = root / "actual"
    actual = json.loads((actual_dir / "process.json").read_text(encoding="utf-8"))
    assert actual["started"] and actual["streamsComplete"] and not actual["watchdog"]
    assert actual["exitCode"] == actual_exit and actual["argv"] == argv
    assert Path(actual["cwd"]).resolve() == (old / "backend/third_party/req").resolve()
    for stream in ("stdout", "stderr"):
        raw = (actual_dir / stream).read_bytes()
        assert len(raw) == actual[stream + "Bytes"] and sha(raw) == actual[stream + "SHA256"]
    for label in ("go-version-m", "go-buildid"):
        command_dir = root / label
        command = json.loads((command_dir / "process.json").read_text(encoding="utf-8"))
        require_success(command)
        assert command["argv"] == (["go", "version", "-m", identity["binary"]] if label == "go-version-m" else ["go", "tool", "buildid", identity["binary"]])
        assert (command_dir / "stdout").read_bytes(), "OLD binary provenance output missing"
        assert not (command_dir / "stderr").read_bytes()
        for stream in ("stdout", "stderr"):
            raw = (command_dir / stream).read_bytes()
            assert len(raw) == command[stream + "Bytes"] and sha(raw) == command[stream + "SHA256"]
    assert (root / "go-version-m/stdout").read_text(encoding="utf-8").splitlines()[0] == identity["binary"] + ": go1.26.10"
    assert identity["auxBefore"] == identity["auxAfter"]
    for label, field in (("aux-before", "auxBefore"), ("aux-after", "auxAfter")):
        seal_dir, seal = root / label, identity[field]
        assert json.loads((seal_dir / "seal.json").read_text(encoding="utf-8")) == seal
        raw = (seal_dir / "metafiles.txt").read_bytes()
        assert sha(raw) == seal["MetadataSHA256"]
        collection = json.loads(raw, object_pairs_hook=unique_object)
        assert set(collection) == {"ImportPaths", "MetaFileFragments"}
        imports, paths = collection["ImportPaths"], collection["MetaFileFragments"]
        assert len(imports) == len(paths) == len(seal["Fragments"]) > 0
        assert len(set(imports)) == len(imports) and len(set(paths)) == len(paths)
        for index, (package, path, fragment) in enumerate(zip(imports, paths, seal["Fragments"])):
            raw = (seal_dir / (str(index) + ".covmeta")).read_bytes()
            assert fragment == {"ImportPath": package, "Path": path, "Bytes": len(raw), "SHA256": sha(raw)} and raw
    assert "-test.gocoverdir=" + identity["auxBefore"]["coverdir"] in argv
    profile = root / "parent-binary.raw.out"
    raw = profile.read_bytes()
    assert len(raw) == identity["parentProfile"]["bytes"] and sha(raw) == identity["parentProfile"]["sha256"]
    assert "-test.coverprofile=" + identity["parentProfile"]["originalPath"] in argv
    assert any(int(line.rsplit(" ", 1)[1]) > 0 for line in raw.decode().splitlines()[1:]), "OLD atomic profile has no executed blocks"
    assert profile_blocks(profile, old) == profile_blocks(directory / "parent.raw.out", old, aggregate=True)
    proof = {"source": head, "tree": tree, "binarySHA256": identity["beforeSHA256"],
             "actualExit": actual_exit, "identity": str((root / "identity.json").relative_to(E)),
             "metadataComplete": True, "atomicProfileSHA256": sha(raw), "auxiliarySealsComplete": True}
    save(directory / "verified-binary-evidence.json", proof)
    return proof


def old_windows_witness():
    report = {"kind": "old-windows-resource-witness", "source": OLD_SOURCE, "tree": OLD_TREE,
              "production": OLD_PRODUCTION, "newSource": SOURCE, "controller": os.environ["GITHUB_SHA"],
              "errors": [], "commands": [], "resourceRED": False,
              "scope": "Separate negative observation; never a passing OLD suite or permission to waive NEW failures"}
    roots = {"controller": (WORKSPACE / "controller", os.environ["GITHUB_SHA"]),
             "baseline": (WORKSPACE / "baseline", BASE),
             "candidate": (WORKSPACE / "source", SOURCE), "old": (WORKSPACE / "old", OLD_SOURCE)}
    before = {}
    try:
        assert SOURCE_READY, "Design controller only: formatted NEW/OLD fixture pins and dual review are pending"
        assert os.name == "nt", "OLD resource witness requires native Windows"
        for label, (root, head) in roots.items():
            before[label] = snapshot(root, head, label + "-before", archive=True)
        assert before["candidate"]["tree"] == TREE
        old, source = WORKSPACE / "old", WORKSPACE / "source"
        old_test_only_inputs(old, source, before["old"])
        require_success(capture(E / "commands/go-version", ["go", "version"], WORKSPACE))
        assert (E / "commands/go-version/stdout").read_text(encoding="utf-8").strip() == "go version go1.26.10 windows/amd64"
        require_success(capture(E / "commands/go-env", ["go", "env", "-json", "GOOS", "GOARCH", "CGO_ENABLED", "CC", "GOTOOLCHAIN", "GOWORK", "GOFLAGS"], WORKSPACE))
        actual_env = json.loads((E / "commands/go-env/stdout").read_text(encoding="utf-8"))
        assert actual_env == {"GOOS": "windows", "GOARCH": "amd64", "CGO_ENABLED": "1", "CC": "gcc",
                              "GOTOOLCHAIN": "local", "GOWORK": "off", "GOFLAGS": "-mod=readonly"}
        compiler_preflight(actual_env)
        windows_storage_preflight()
        exec_controls()
        module_graph(WORKSPACE / "baseline", old)
        wrapper = Path(__file__).resolve()
        assert '"' not in str(wrapper) and '"' not in sys.executable
        exec_flag = '"' + sys.executable + '" "' + str(wrapper) + '" exec'
        observed = {}
        for phase, selector in (("old-calibration", "^TestReqDumpOwnershipWindowsCalibration$"),
                                ("old-resource", "^TestReqDumpOwnershipFileLifecycle$/^(request_sync|client_sync)$")):
            directory = E / "phases" / phase
            directory.mkdir(parents=True)
            env = dict(os.environ, G_OBSERVATION_PHASE=phase, G_OBSERVATION_SOURCE_HEAD=OLD_SOURCE, G_OBSERVATION_SOURCE_TREE=OLD_TREE)
            argv = ["go", "test", "-p=1", "-parallel=1", "-race", "-count=1", "-timeout=3m", "-json",
                    "-covermode=atomic", "-coverpkg=" + MODULE + "/...",
                    "-coverprofile=" + str(directory / "parent.raw.out"), "-exec=" + exec_flag,
                    "-run=" + selector, MODULE]
            command = capture(directory / "command", argv, old / "backend", timeout=600, env=env)
            report["commands"].append(command)
            assert command["started"] and command["streamsComplete"] and not command["watchdog"]
            raw = (directory / "command/stdout").read_bytes()
            stderr = (directory / "command/stderr").read_bytes()
            assert not any(marker in raw + stderr for marker in
                           (b"WARNING: DATA RACE", b"panic:", b"fatal error:", b"test timed out", b"G_CHILD_WATCHDOG")), "Race/timeout/launch failure is not a resource RED"
            events = [json.loads(line) for line in raw.decode("utf-8").splitlines()]
            ledger = old_event_ledger(events, phase)
            binary = old_binary_evidence(directory, phase, selector, old, 0 if phase == "old-calibration" else 1)
            package_failures = [event for event in events if event.get("Package") == MODULE and event.get("Action") == "fail" and not event.get("Test")]
            if phase == "old-calibration":
                require_success(command)
                name = "TestReqDumpOwnershipWindowsCalibration"
                selected = [event for event in events if event.get("Package") == MODULE and event.get("Test") == name]
                assert sum(event.get("Action") == "run" for event in selected) == 1
                assert sum(event.get("Action") == "pass" for event in selected) == 1
                assert not any(event.get("Action") == "fail" for event in events)
                output = "".join(event.get("Output", "") for event in selected)
                assert re.search(r"DUMP1752_RESOURCE_DENIED open os.Create control: type=\*[^ ]*PathError operation=remove errno=(5|32)(?:\s|$)", output)
                assert output.count("DUMP1752_RESOURCE_REMOVE_OK operation=remove") == 1
                observed[phase] = {"actualExit": command["exitCode"], "calibratedWindowsDenialAndSuccessfulCloseRemove": True,
                                   "ledger": ledger, "binaryEvidence": binary}
            else:
                # A known actual test failure is preserved, not converted into
                # a passing OLD test. Only these exact resource failures count.
                assert command["exitCode"] == 1 and len(package_failures) == 1
                names = {"TestReqDumpOwnershipFileLifecycle/request_sync", "TestReqDumpOwnershipFileLifecycle/client_sync"}
                records = []
                for name in sorted(names):
                    selected = [event for event in events if event.get("Package") == MODULE and event.get("Test") == name]
                    output = "".join(event.get("Output", "") for event in selected)
                    assert sum(event.get("Action") == "run" for event in selected) == 1
                    assert sum(event.get("Action") == "fail" for event in selected) == 1
                    assert not any(event.get("Action") == "pass" for event in selected)
                    assert re.search(r"DUMP1752_OUTPUT bytes=[1-9][0-9]* sha256=[0-9a-f]{64}", output)
                    assert re.search(r"DUMP1752_RESOURCE_REMOVE_ERR type=\*[^ ]*PathError operation=remove errno=(5|32)(?:\s|$)", output)
                    assert "DUMP1752_RESOURCE_REMOVE:" in output
                    assert "DUMP1752_RESOURCE_REMOVE_OK" not in output
                    records.append({"test": name, "actualRun": 1, "actualFail": 1,
                                    "actualTypedRemoveDenial": True,
                                    "prerequisites": "Identical frozen FileLifecycle source validates real peer/status/body/captured upload+response before this actual Remove; raw OUTPUT bytes/SHA recorded"})
                observed[phase] = {"actualExit": command["exitCode"], "cases": records, "ledger": ledger, "binaryEvidence": binary}
        report["observed"] = observed
        report["resourceRED"] = True
    except Exception:
        report["errors"].append(traceback.format_exc())
    finally:
        for label, (root, head) in roots.items():
            try:
                after = snapshot(root, head, label + "-after")
                assert before[label] == after
            except Exception:
                report["errors"].append({"check": label + "-after-freeze", "error": traceback.format_exc()})
        save(E / "actual-old-resource-witness.json", report)
    return 0 if report["resourceRED"] and not report["errors"] else 1



def old_tls_test_only_inputs(old, source, frozen):
    """The race witness retains full e1 production and the five identical tests."""
    assert git(old, "rev-parse", "HEAD^{tree}").decode().strip() == OLD_TLS_TREE
    assert git(old, "show", "-s", "--format=%P", "HEAD").decode().split() == [OLD_TLS_PRODUCTION]
    anchor = {}
    for row in git(old, "ls-tree", "--full-tree", "-r", "-z", OLD_TLS_PRODUCTION).split(b"\0"):
        if row:
            header, name = row.split(b"\t", 1)
            mode, kind, blob = header.decode().split()
            assert kind == "blob"
            anchor[os.fsdecode(name)] = (mode, blob)
    current = {item["path"]: item for item in frozen["files"]}
    paths = {"backend/third_party/req/" + name for name in OLD_TLS_FIXTURES}
    manifest_path = "backend/third_party/req/UPSTREAM-MANIFEST.json"
    assert not set(anchor) - set(current)
    assert set(current) - set(anchor) == paths, "OLD TLS may add only five identical fixtures"
    assert all((current[name]["mode"], current[name]["blob"]) == value
               for name, value in anchor.items() if name != manifest_path), "OLD TLS production/original tests changed"
    assert current[manifest_path]["mode"] == anchor[manifest_path][0]
    seals = {}
    for name in OLD_TLS_FIXTURES:
        relative = "backend/third_party/req/" + name
        raw = (old / relative).read_bytes()
        assert raw == (source / relative).read_bytes(), "OLD/NEW TLS fixture bytes differ"
        seals[name] = sha(raw)
    original = json.loads(git(old, "show", OLD_TLS_PRODUCTION + ":" + manifest_path))
    actual = json.loads((old / manifest_path).read_text(encoding="utf-8"))
    assert set(actual) == set(original) | {"customTLSWitnessControl"}
    assert all(actual[name] == value for name, value in original.items() if name != "newOwnedFiles")
    assert actual["newOwnedFiles"] == original["newOwnedFiles"] + list(OLD_TLS_FIXTURES)
    assert actual["customTLSWitnessControl"] == {
        "productionHead": OLD_TLS_PRODUCTION, "fixtureSHA256": seals,
        "scope": "test-only H1/H2 custom TLS race witness; no production correction"}
    frames = {"HTTP1": ("transport.go", 2082, "pconn.conn = conn", 2091, "pconn.conn.Close()"),
              "HTTP2": ("internal/http2/transport.go", 642, "conn = tlsCn", 650, "conn.Close()")}
    for path, write_line, write_source, read_line, read_source in frames.values():
        lines = (old / "backend/third_party/req" / path).read_text(encoding="utf-8").splitlines()
        assert lines[write_line - 1].strip() == write_source
        assert lines[read_line - 1].strip() == read_source
    save(E / "old-tls-test-only-inputs.json", {"oldHead": OLD_TLS_SOURCE, "oldTree": OLD_TLS_TREE,
         "productionHead": OLD_TLS_PRODUCTION, "fixtureSHA256": seals, "actualFieldFrames": frames,
         "policy": "Every other tracked mode/blob equals preserved e1; original production/tests/manifest declarations unchanged"})


def old_tls_race_frames(output, protocol):
    """Require a same-address production write/read pair in each actual race report."""
    path, function, write_line, read_line = (
        ("/third_party/req/transport.go", "customTlsHandshake", 2082, 2091) if protocol == "HTTP1" else
        ("/third_party/req/internal/http2/transport.go", "dialTLSWithContext", 642, 650))
    records = []
    for block in output.split("WARNING: DATA RACE\n")[1:]:
        block = block.split("==================", 1)[0]
        accesses = list(re.finditer(r"(?i:(?:Previous )?(Read|Write)) at (0x[0-9a-f]+) by goroutine [0-9]+:\n", block))
        actual = []
        for index, match in enumerate(accesses):
            end = accesses[index + 1].start() if index + 1 < len(accesses) else len(block)
            stack = block[match.end():end].split("\nGoroutine ", 1)[0]
            kind = match[1].capitalize()
            line = write_line if kind == "Write" else read_line
            module_function = MODULE + ("/internal/http2" if protocol == "HTTP2" else "") + ".(*Transport)." + function
            if re.search(re.escape(module_function) + r"(?:\.func[0-9]+)?\(.*\)\n\s+[^\n]*" + re.escape(path) + ":" + str(line) + r"(?:\s|\+)", stack.replace("\\", "/")):
                actual.append({"kind": kind, "address": match[2], "sourceLine": line})
        if len(actual) == 2 and {item["kind"] for item in actual} == {"Read", "Write"} and len({item["address"] for item in actual}) == 1:
            records.append({"protocol": protocol, "function": function, "sourcePath": path,
                            "accesses": actual, "rawRaceReportSHA256": sha(block.encode())})
    assert records, "No actual same-address OLD production read/write race pair for " + protocol
    return records


def old_tls_witness():
    report = {"kind": "old-custom-tls-race-witness", "source": OLD_TLS_SOURCE, "tree": OLD_TLS_TREE,
              "production": OLD_TLS_PRODUCTION, "newSource": SOURCE, "controller": os.environ["GITHUB_SHA"],
              "errors": [], "commands": [], "raceRED": False,
              "scope": "Separate actual OLD negative observation; never waives any NEW test/race/resource/85 failure"}
    roots = {"controller": (WORKSPACE / "controller", os.environ["GITHUB_SHA"]),
             "baseline": (WORKSPACE / "baseline", BASE), "candidate": (WORKSPACE / "source", SOURCE),
             "old-tls": (WORKSPACE / "old-tls", OLD_TLS_SOURCE)}
    before = {}
    try:
        assert SOURCE_READY, "Design controller only: formatted NEW/OLD TLS pins and dual review pending"
        assert os.name != "nt", "OLD custom TLS witness uses the isolated Linux race job"
        for label, (root, head) in roots.items():
            before[label] = snapshot(root, head, label + "-before", archive=True)
        assert before["candidate"]["tree"] == TREE
        old, source = WORKSPACE / "old-tls", WORKSPACE / "source"
        old_tls_test_only_inputs(old, source, before["old-tls"])
        require_success(capture(E / "commands/go-version", ["go", "version"], WORKSPACE))
        assert (E / "commands/go-version/stdout").read_text(encoding="utf-8").strip() == "go version go1.26.10 linux/amd64"
        require_success(capture(E / "commands/go-env", ["go", "env", "-json", "GOOS", "GOARCH", "CGO_ENABLED", "CC", "GOTOOLCHAIN", "GOWORK", "GOFLAGS"], WORKSPACE))
        actual_env = json.loads((E / "commands/go-env/stdout").read_text(encoding="utf-8"))
        assert actual_env == {"GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "1", "CC": "gcc",
                              "GOTOOLCHAIN": "local", "GOWORK": "off", "GOFLAGS": "-mod=readonly"}
        compiler_preflight(actual_env)
        exec_controls()
        module_graph(WORKSPACE / "baseline", old)
        wrapper = Path(__file__).resolve()
        assert '"' not in str(wrapper) and '"' not in sys.executable
        exec_flag = '"' + sys.executable + '" "' + str(wrapper) + '" exec'
        phase = "old-tls-race"
        selector = "^TestReqCustomTLS_LatePublicationRace$/^(HTTP1|HTTP2)$"
        directory = E / "phases" / phase
        directory.mkdir(parents=True)
        env = dict(os.environ, G_OBSERVATION_PHASE=phase, G_OBSERVATION_SOURCE_HEAD=OLD_TLS_SOURCE,
                   G_OBSERVATION_SOURCE_TREE=OLD_TLS_TREE)
        argv = ["go", "test", "-p=1", "-parallel=1", "-race", "-count=1", "-timeout=3m", "-json",
                "-covermode=atomic", "-coverpkg=" + MODULE + "/...",
                "-coverprofile=" + str(directory / "parent.raw.out"), "-exec=" + exec_flag,
                "-run=" + selector, MODULE]
        command = capture(directory / "command", argv, old / "backend", timeout=600, env=env)
        report["commands"].append(command)
        assert command["started"] and command["streamsComplete"] and not command["watchdog"] and command["exitCode"] == 1
        raw, stderr = (directory / "command/stdout").read_bytes(), (directory / "command/stderr").read_bytes()
        assert not any(marker in raw + stderr for marker in (b"panic:", b"fatal error:", b"test timed out", b"G_CHILD_WATCHDOG"))
        events = [json.loads(line) for line in raw.decode("utf-8").splitlines()]
        ledger = old_event_ledger(events, phase)
        binary = old_binary_evidence(directory, phase, selector, old, 1, head=OLD_TLS_SOURCE, tree=OLD_TLS_TREE)
        # test2json emits the race detector's original stack records at package
        # scope, without a Test field. Preserve all actual output records rather
        # than guessing their child association; exact source frames identify H1/H2.
        all_output = "".join(event.get("Output", "") for event in events) + stderr.decode("utf-8")
        report["raceOutputProvenance"] = {"actualGoJSONSHA256": sha(raw),
             "actualStderrSHA256": sha(stderr), "assembledOutputSHA256": sha(all_output.encode("utf-8")),
             "packageOutputRecords": sum(event.get("Action") == "output" and not event.get("Test") for event in events),
             "policy": "Unmodified actual Go JSON Output records in order plus actual stderr; no invented child assignment"}
        records = []
        for protocol in ("HTTP1", "HTTP2"):
            name = "TestReqCustomTLS_LatePublicationRace/" + protocol
            output = "".join(event.get("Output", "") for event in events if event.get("Test") == name)
            version, alpn = ("1", r'(?:|http/1\.1)') if protocol == "HTTP1" else ("2", "h2")
            assert re.search(r"CUSTOM_TLS_REAL_HANDSHAKE protocol=" + version + r' version=[0-9a-f]+ alpn="' + alpn + r'" raw=[^;\s]+; actual timeout=.+', output), "Missing genuine Firefox TLS and typed-timeout prerequisite marker"
            pairs = old_tls_race_frames(all_output, protocol)
            # OLD closes only the raw socket before its racy late publication;
            # the identical fixture can then Fatal waiting for observed.Close.
            # This exact secondary acknowledgement failure is not race evidence.
            # Genuine handshake/typed-timeout and both production access frames
            # above remain mandatory, as do the actual FAIL/exit1/binary gates.
            secondary = re.findall(r"CUSTOM_TLS_PREREQUISITE: ([^\r\n]*)", output)
            allowed_secondary = "timed out waiting for late returned wrapper cleanup"
            assert len(secondary) <= 1 and all(message == allowed_secondary for message in secondary), "Unexpected OLD TLS prerequisite failure"
            assert "CUSTOM_TLS_TIMEOUT:" not in output, "OLD TLS typed-timeout prerequisite failed"
            records.append({"test": name, "actualRun": 1, "actualFail": 1,
                            "actualTypedTimeoutAndRealTLS": True, "productionRacePairs": pairs,
                            "secondaryOldResourceAcknowledgementFailures": secondary})
        matched_blocks = [pair["rawRaceReportSHA256"] for item in records for pair in item["productionRacePairs"]]
        assert len(matched_blocks) == len(set(matched_blocks)) == all_output.count("WARNING: DATA RACE\n"), "Unexpected, duplicate or unclassified OLD race report"
        assert "CUSTOM_TLS_TIMEOUT:" not in all_output
        assert all_output.count("CUSTOM_TLS_PREREQUISITE:") == sum(len(item["secondaryOldResourceAcknowledgementFailures"]) for item in records), "OLD TLS prerequisite outside the two verified child witnesses"
        report.update({"observed": records, "ledger": ledger, "binaryEvidence": binary, "raceRED": True})
    except Exception:
        report["errors"].append(traceback.format_exc())
    finally:
        for label, (root, head) in roots.items():
            try:
                after = snapshot(root, head, label + "-after")
                assert before[label] == after
            except Exception:
                report["errors"].append({"check": label + "-after-freeze", "error": traceback.format_exc()})
        save(E / "actual-old-tls-race-witness.json", report)
    return 0 if report["raceRED"] and not report["errors"] else 1


def archive_script_coverage(root, directory, actual):
    """Verify and archive only the unchanged script's owned coverage copy."""
    generated = root / "backend/coverage-req-http2.out"
    if successful(actual):
        assert generated.is_file() and not generated.is_symlink(), "Formal script coverage copy missing"
    if generated.exists() or generated.is_symlink():
        assert generated.is_file() and not generated.is_symlink()
        raw = generated.read_bytes()
        mapped = directory / "all.mapped.out"
        assert raw == mapped.read_bytes(), "Unexpected ignored formal coverage output"
        archived = directory / "checkout-coverage-req-http2.out"
        archived.write_bytes(raw)
        assert archived.read_bytes() == raw
        save(directory / "generated-output.json", {"path": str(generated),
             "bytes": len(raw), "sha256": sha(raw), "archive": str(archived),
             "policy": "Only this verified script copy is removed; all other residual inputs fail final snapshots"})
        generated.unlink()


def observe(kind):
    report = {"kind": kind, "source": BASE if kind == "baseline" else SOURCE, "baseline": BASE,
              "controller": os.environ["GITHUB_SHA"], "errors": [], "commands": []}
    before = {}
    roots = {"controller": (WORKSPACE / "controller", os.environ["GITHUB_SHA"]),
             "baseline": (WORKSPACE / "baseline", BASE)}
    if kind == "candidate":
        roots["candidate"] = (WORKSPACE / "source", SOURCE)
    try:
        assert SOURCE_READY, "Design controller only: formatted NEW/OLD fixture pins and dual review are pending; do not dispatch"
        for label, (root, head) in roots.items():
            before[label] = snapshot(root, head, label + "-before", archive=True)
        version = capture(E / "commands/go-version", ["go", "version"], WORKSPACE)
        require_success(version)
        platform = "windows/amd64" if os.name == "nt" else "linux/amd64"
        assert (E / "commands/go-version/stdout").read_text(encoding="utf-8").strip() == "go version go1.26.10 " + platform
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
            archive_script_coverage(WORKSPACE / "baseline", E / "original401", result)
            require_success(result)
        else:
            source, baseline = WORKSPACE / "source", WORKSPACE / "baseline"
            assert before["candidate"]["tree"] == TREE
            current = {item["path"]: item for item in before["candidate"]["files"]}
            anchor = {}
            for row in git(source, "ls-tree", "--full-tree", "-r", "-z", PRODUCTION).split(b"\0"):
                if row:
                    header, name = row.split(b"\t", 1)
                    mode, kind, blob = header.decode().split()
                    assert kind == "blob"
                    anchor[os.fsdecode(name)] = (mode, blob)
            assert not set(anchor) - set(current), "Infrastructure omitted production inputs"
            assert set(current) - set(anchor) == {name for name in INFRA_PATHS if name.startswith(".github/")}
            assert all((current[path]["mode"], current[path]["blob"]) == value for path, value in anchor.items() if path not in INFRA_PATHS), "Infrastructure altered production/business inputs"
            assert subprocess.run(["git", "-C", str(source), "merge-base", "--is-ancestor", PRODUCTION, SOURCE]).returncode == 0
            provenance = json.loads((source / "backend/third_party/req/DOWNLOAD-CONTRACTS-PROVENANCE.json").read_text(encoding="utf-8"))
            helper = (source / "backend/third_party/req/download_wire_peer_test.go").read_bytes()
            assert provenance["files"]["download_wire_peer_test.go"] == {"bytes": len(helper), "sha256": sha(helper)}
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
            original_controls = source / "backend/tools/req-original-test-adaptation-controls.py"
            require_success(capture(E / "commands/original-test-adaptation-controls",
                                    [sys.executable, "-B", str(original_controls), "--backend", str(source / "backend"),
                                     "--evidence", str(E / "original-test-adaptation-controls"), "--actual-inputs", str(preflight)],
                                    source / "backend", timeout=300))
            controls = Path(__file__).with_name("controls.py")
            require_success(capture(E / "commands/integrity-controls", [sys.executable, "-B", str(controls), str(source)], WORKSPACE, timeout=1000))
            selector = "^(TestReqDownloadContract|TestReqDownloadContractCancellation|TestReqParallelDownloadContract"
            selector += "|TestReqParallelDownloadFallback|TestReqParallelDownloadHTTP3Cleanup|TestReqParallelDownloadInProcessContract"
            selector += "|TestReqParallelDownloadDefaultOutput"
            selector += "|TestReqDumpOwnership.*|TestReqHTTP2ReceiveFlowContract_.*|TestReqCustomTLS_.*"
            selector += "|TestReqPublicAPI_.*|TestReqH1Boundary_.*|TestReqPublicDefaultContract.*|TestReqBisectContract_.*"
            selector += "|TestReqHTTP2CoverageContract_.*|TestReqHTTP2CoverageFrame_.*|TestReqSafariWrapperContract.*"
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
                    argv += ["-run=" + selector, MODULE, MODULE + "/internal/http2", MODULE + "/internal/bisect"]
                else:
                    argv += [MODULE + "/..."]
                result = capture(phase_dir / "command", argv, source / "backend", timeout=2000 if phase == "named" else 3000, env=env)
                report["commands"].append(result)
                for label, check in (("actual-command", lambda: require_success(result)),
                                     ("inventory", lambda: actual_inventory(phase, source, os.name == "nt")),
                                     ("raw-parent-profile", lambda: profile_blocks(phase_dir / "parent.raw.out", source, aggregate=True)),
                                     ("child-profiles", lambda: child_profiles(phase, source, os.name == "nt"))):
                    try:
                        check()
                    except Exception:
                        report["errors"].append({"phase": phase, "check": label, "error": traceback.format_exc()})
            if os.name != "nt":
                # The observer is not a replacement for the unchanged source
                # script and all18 production adaptation per-file85 obligations. Its two req profiles
                # do not supply the separate PR Unit/Integration profiles.
                env = dict(os.environ, REQ_SECURITY_EVIDENCE=str(E / "formal-new"),
                           REQ_SECURITY_EXPECTED_HEAD=SOURCE)
                actual = capture(E / "commands/formal-new", ["bash", "tools/req-http2-security-ci.sh"],
                                 source / "backend", timeout=3000, env=env)
                report["commands"].append(actual)
                archive_script_coverage(source, E / "formal-new", actual)
                require_success(actual)
    except Exception:
        report["errors"].append({"check": "prerequisite-or-observation", "error": traceback.format_exc()})
    finally:
        for label, (root, head) in roots.items():
            try:
                after = snapshot(root, head, label + "-after")
                assert before[label] == after
            except Exception:
                report["errors"].append({"check": label + "-after-freeze", "error": traceback.format_exc()})
        report["policy"] = "Every NEW actual failure fails this job; no accepted-RED list or profile counter merge. Exact673 retained468 baseline, original401 within it, retained526+H2six+dump42=574 plus distinct rootTLS18/privateH2TLS13=605 are preserved as an exact separate subset of registered880. Expansion275 and focused412/413 are distinct obligations, never substitutes for full880 or legacy605. Child profiles never contribute counters to the original four profiles or all18 production adaptation per-file85 gates (original16 plus authorized Safari wrapper and bisect). OLD resource and OLD TLS race observations are separate and cannot waive NEW failures."
        save(E / "actual-observation.json", report)
    return 1 if report["errors"] else 0


if __name__ == "__main__":
    if sys.argv[1] == "exec":
        sys.exit(exec_mode(sys.argv[2:]))
    if sys.argv[1] == "control-exec":
        sys.exit(exec_mode(sys.argv[3:], timeout=float(sys.argv[2])))
    if sys.argv[1] == "old-tls-witness":
        sys.exit(old_tls_witness())
    if sys.argv[1] == "old-witness":
        sys.exit(old_windows_witness())
    sys.exit(observe(sys.argv[1]))
