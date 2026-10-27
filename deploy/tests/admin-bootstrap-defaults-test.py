#!/usr/bin/env python3
"""Run deployment preparation in isolated fixtures; CI only, no network."""

import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile


DEPLOY = Path(__file__).resolve().parents[1]
OPENSSL = shutil.which("openssl")
assert OPENSSL, "CI must provide real OpenSSL"

for name in (
    "docker-compose.yml", "docker-compose.local.yml",
    "docker-compose.standalone.yml", "docker-compose.dev.yml",
):
    text = (DEPLOY / name).read_text()
    assert "ADMIN_EMAIL=${ADMIN_EMAIL:-}" in text, name
    assert "ADMIN_EMAIL=${ADMIN_EMAIL:-admin@" not in text, name
assert "ADMIN_EMAIL=\n" in (DEPLOY / ".env.example").read_text()


def fixture(directory, fail_entropy=False):
    fake_bin = directory / "bin"
    fake_bin.mkdir()
    curl = fake_bin / "curl"
    curl.write_text('''#!/usr/bin/env python3
import os, pathlib, shutil, sys
args = sys.argv[1:]
destination = args[args.index("-o") + 1]
source = next(arg for arg in args if arg.startswith("https://"))
name = ".env.example" if source.endswith("/.env.example") else "docker-compose.local.yml"
shutil.copyfile(pathlib.Path(os.environ["FIXTURE_DEPLOY"]) / name, destination)
''')
    curl.chmod(0o755)
    if fail_entropy:
        openssl = fake_bin / "openssl"
        openssl.write_text('''#!/usr/bin/env python3
import os, sys
if sys.argv[1:] == ["rand", "-hex", "6"]:
    sys.exit(1)
os.execv(os.environ["FIXTURE_OPENSSL"], [os.environ["FIXTURE_OPENSSL"], *sys.argv[1:]])
''')
        openssl.chmod(0o755)
    return dict(os.environ, PATH=str(fake_bin) + os.pathsep + os.environ["PATH"],
                FIXTURE_DEPLOY=str(DEPLOY), FIXTURE_OPENSSL=OPENSSL)


emails = []
for index in range(2):
    with tempfile.TemporaryDirectory(prefix="setup-admin-ci-") as temp:
        directory = Path(temp)
        result = subprocess.run(["bash", str(DEPLOY / "docker-deploy.sh")],
                                cwd=directory, env=fixture(directory),
                                input="", text=True, capture_output=True, timeout=20)
        assert result.returncode == 0, "deployment fixture failed"
        env_file = (directory / ".env").read_text()
        email = re.search(r"^ADMIN_EMAIL=(.*)$", env_file, re.MULTILINE).group(1)
        assert re.fullmatch(r"admin-[0-9a-f]{12}@sub2api\.local", email), "email must use actual entropy"
        emails.append(email)
        # Existing deployment cancellation must retain the administrator value.
        before = env_file.replace("ADMIN_EMAIL=" + email, "ADMIN_EMAIL=owner@example.com")
        (directory / ".env").write_text(before)
        result = subprocess.run(["bash", str(DEPLOY / "docker-deploy.sh")],
                                cwd=directory, env=dict(os.environ, PATH=str(directory / "bin") + os.pathsep + os.environ["PATH"], FIXTURE_DEPLOY=str(DEPLOY)),
                                input="N\n", text=True, capture_output=True, timeout=20)
        assert result.returncode == 0
        assert (directory / ".env").read_text() == before, "canceled upgrade must preserve existing credentials"
assert emails[0] != emails[1], "independent fresh preparations must not share a fixed email"

with tempfile.TemporaryDirectory(prefix="setup-admin-ci-failure-") as temp:
    directory = Path(temp)
    result = subprocess.run(["bash", str(DEPLOY / "docker-deploy.sh")],
                            cwd=directory, env=fixture(directory, fail_entropy=True),
                            input="", text=True, capture_output=True, timeout=20)
    assert result.returncode != 0, "admin entropy failure must abort preparation"
    assert not (directory / ".env").exists(), "failed entropy must not persist fallback credentials"

print("PASS: four compose defaults, fresh random email, canceled upgrade preservation, entropy failure")
