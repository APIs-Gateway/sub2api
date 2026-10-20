#!/usr/bin/env python3
"""Lightweight repository secret scanner for local submit checks.

The scanner is intentionally conservative: it catches high-confidence secrets
without failing on the many synthetic API keys used in tests and docs.
"""

from __future__ import annotations

import math
import os
import re
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
MAX_FILE_BYTES = 1_000_000

EXCLUDED_DIRS = {
    ".git",
    ".pnpm-store",
    ".serena",
    ".venv",
    "backend/bin",
    "backend/data",
    "backend/internal/web/dist",
    "dist",
    "frontend/coverage",
    "frontend/dist",
    "frontend/node_modules",
    "node_modules",
    "vendor",
}

TEST_PATH_MARKERS = (
    "__tests__/",
    "_test.go",
    ".spec.ts",
    ".spec.tsx",
    ".test.ts",
    ".test.tsx",
)

ALLOW_VALUE_PATTERNS = (
    re.compile(r"\$\{\{\s*secrets\.[A-Za-z0-9_]+\s*\}\}"),
    re.compile(r"^(?:x+|X+|0+|1+|a+|A+)$"),
    re.compile(r"^change-this-", re.I),
    re.compile(r"^https?://", re.I),
    re.compile(r"^(?:example|placeholder|changeme|dummy|test|fake|redacted)$", re.I),
)

SECRET_PATTERNS: tuple[tuple[str, re.Pattern[str]], ...] = (
    ("private key block", re.compile(r"-----BEGIN (?:[A-Z0-9 ]+ )?PRIVATE KEY-----")),
    ("AWS access key id", re.compile(r"\bAKIA[0-9A-Z]{16}\b")),
    ("AWS secret access key", re.compile(r"\b(?:aws_secret_access_key|AWS_SECRET_ACCESS_KEY)\s*[:=]\s*['\"]?([A-Za-z0-9/+=]{40})['\"]?")),
    ("GitHub token", re.compile(r"\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9_]{36,}\b")),
    ("Slack token", re.compile(r"\bxox[baprs]-[A-Za-z0-9-]{20,}\b")),
    ("Google API key", re.compile(r"\bAIza[0-9A-Za-z_-]{35}\b")),
    ("OpenAI project key", re.compile(r"\bsk-proj-[A-Za-z0-9_-]{40,}\b")),
    ("OpenAI API key", re.compile(r"\bsk-[A-Za-z0-9]{48,}\b")),
    ("Anthropic API key", re.compile(r"\bsk-ant-[A-Za-z0-9_-]{40,}\b")),
)

GENERIC_ASSIGNMENT = re.compile(
    r"(?i)\b(?:api[_-]?key|secret|token|password|passwd|private[_-]?key)\b"
    r"\s*[:=]\s*['\"]([^'\"\n]{32,})['\"]"
)


def git_files() -> list[Path]:
    proc = subprocess.run(
        ["git", "ls-files", "-co", "--exclude-standard"],
        cwd=ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    if proc.returncode != 0:
        print(proc.stderr, file=sys.stderr)
        sys.exit(proc.returncode)
    return [ROOT / line for line in proc.stdout.splitlines() if line]


def rel(path: Path) -> str:
    return path.relative_to(ROOT).as_posix()


def is_excluded(path: Path) -> bool:
    r = rel(path)
    return any(r == d or r.startswith(d + "/") for d in EXCLUDED_DIRS)


def is_test_path(path: Path) -> bool:
    r = rel(path)
    return any(marker in r or r.endswith(marker) for marker in TEST_PATH_MARKERS)


def entropy(value: str) -> float:
    if not value:
        return 0.0
    counts = {ch: value.count(ch) for ch in set(value)}
    length = len(value)
    return -sum((count / length) * math.log2(count / length) for count in counts.values())


def line_number(text: str, offset: int) -> int:
    return text.count("\n", 0, offset) + 1


def is_allowed_value(value: str) -> bool:
    stripped = value.strip()
    if any(ch.isspace() for ch in stripped):
        return True
    return any(pattern.search(stripped) for pattern in ALLOW_VALUE_PATTERNS)


def scan_file(path: Path) -> list[str]:
    if is_excluded(path) or not path.is_file():
        return []
    try:
        if path.stat().st_size > MAX_FILE_BYTES:
            return []
        data = path.read_bytes()
    except OSError:
        return []
    if b"\x00" in data:
        return []
    try:
        text = data.decode("utf-8")
    except UnicodeDecodeError:
        text = data.decode("utf-8", errors="ignore")

    findings: list[str] = []
    test_file = is_test_path(path)

    for name, pattern in SECRET_PATTERNS:
        if test_file and name == "private key block":
            continue
        for match in pattern.finditer(text):
            value = match.group(1) if match.lastindex else match.group(0)
            if is_allowed_value(value):
                continue
            findings.append(f"{rel(path)}:{line_number(text, match.start())}: {name}")

    if not test_file:
        for match in GENERIC_ASSIGNMENT.finditer(text):
            value = match.group(1)
            if is_allowed_value(value):
                continue
            if entropy(value) >= 3.5:
                findings.append(f"{rel(path)}:{line_number(text, match.start())}: suspicious secret assignment")

    return findings


def main() -> int:
    findings: list[str] = []
    for path in git_files():
        findings.extend(scan_file(path))

    if findings:
        print("Potential secrets found:")
        for finding in findings:
            print(f"  - {finding}")
        return 1

    print("Secret scan passed.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
