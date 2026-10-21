#!/usr/bin/env python3
import argparse
import json
import sys
from datetime import date


HIGH_SEVERITIES = {"high", "critical"}
REQUIRED_FIELDS = {"package", "advisory", "severity", "mitigation", "expires_on"}


def split_kv(line: str) -> tuple[str, str]:
    # 解析 "key: value" 形式的简单 YAML 行，并去除引号。
    key, value = line.split(":", 1)
    value = value.strip()
    if (value.startswith('"') and value.endswith('"')) or (
        value.startswith("'") and value.endswith("'")
    ):
        value = value[1:-1]
    return key.strip(), value


def parse_exceptions(path: str) -> list[dict]:
    # 轻量解析异常清单，避免引入额外依赖。
    exceptions = []
    current = None
    with open(path, "r", encoding="utf-8") as handle:
        for raw in handle:
            line = raw.strip()
            if not line or line.startswith("#"):
                continue
            if line.startswith("version:") or line.startswith("exceptions:"):
                continue
            if line.startswith("- "):
                if current:
                    exceptions.append(current)
                current = {}
                line = line[2:].strip()
                if line:
                    key, value = split_kv(line)
                    current[key] = value
                continue
            if current is not None and ":" in line:
                key, value = split_kv(line)
                current[key] = value
    if current:
        exceptions.append(current)
    return exceptions


def pick_advisory_id(advisory: dict) -> str | None:
    # 优先使用可稳定匹配的标识（GHSA/URL/CVE），避免误匹配到其他同名漏洞。
    return (
        advisory.get("github_advisory_id")
        or advisory.get("url")
        or (advisory.get("cves") or [None])[0]
        or (str(advisory.get("id")) if advisory.get("id") is not None else None)
        or advisory.get("title")
        or advisory.get("advisory")
        or advisory.get("overview")
    )


def iter_vulns(data: dict):
    # 兼容 pnpm audit 的不同输出结构（advisories / vulnerabilities），并提取 advisory 标识。
    advisories = data.get("advisories")
    if isinstance(advisories, dict):
        for advisory in advisories.values():
            name = advisory.get("module_name") or advisory.get("name")
            severity = advisory.get("severity")
            advisory_id = pick_advisory_id(advisory)
            title = (
                advisory.get("title")
                or advisory.get("advisory")
                or advisory.get("overview")
                or advisory.get("url")
            )
            yield name, severity, advisory_id, title

    vulnerabilities = data.get("vulnerabilities")
    if isinstance(vulnerabilities, dict):
        for name, vuln in vulnerabilities.items():
            severity = vuln.get("severity")
            via = vuln.get("via", [])
            titles = []
            advisories = []
            if isinstance(via, list):
                for item in via:
                    if isinstance(item, dict):
                        advisories.append(
                            item.get("github_advisory_id")
                            or item.get("url")
                            or item.get("source")
                            or item.get("title")
                            or item.get("name")
                        )
                        titles.append(
                            item.get("title")
                            or item.get("url")
                            or item.get("advisory")
                            or item.get("source")
                        )
                    elif isinstance(item, str):
                        advisories.append(item)
                        titles.append(item)
            elif isinstance(via, str):
                advisories.append(via)
                titles.append(via)
            title = "; ".join([t for t in titles if t])
            for advisory_id in [a for a in advisories if a]:
                yield name, severity, advisory_id, title


def normalize_severity(severity: str) -> str:
    # 统一大小写，避免比较失败。
    return (severity or "").strip().lower()


def normalize_package(name: str) -> str:
    # 包名只去掉首尾空白，保留原始大小写，同时兼容非字符串输入。
    if name is None:
        return ""
    return str(name).strip()


def normalize_advisory(advisory: str) -> str:
    # advisory 统一为小写匹配，避免 GHSA/URL 因大小写差异导致漏匹配。
    # pnpm 的 source 字段可能是数字，这里统一转为字符串以保证可比较。
    if advisory is None:
        return ""
    return str(advisory).strip().lower()


def parse_date(value: str) -> date | None:
    # 仅接受 ISO8601 日期格式，非法值视为无效。
    try:
        return date.fromisoformat(value)
    except ValueError:
        return None


def validate_report(audit: object, audit_exit: str) -> None:
    """Accept the complete pnpm 9 --json report, never an error response.

    pnpm v9.15.9 audit.ts returns 1 for ANY nonzero metadata vulnerability
    count in JSON mode, even below --audit-level. Counts describe dependency
    occurrences, so they need not equal the number of distinct advisories.
    """
    def require(condition: bool, message: str) -> None:
        if not condition:
            raise ValueError(message)

    def count(value: object) -> bool:
        return type(value) is int and value >= 0

    def text(value: object) -> bool:
        return isinstance(value, str) and bool(value.strip())

    require(isinstance(audit, dict), "top level must be a pnpm 9 report object")
    require(not ({"error", "errors", "vulnerabilities"} & audit.keys()),
            "error containers or unsupported report format")
    require(isinstance(audit.get("actions"), list), "missing/invalid actions")
    require(isinstance(audit.get("muted"), list), "missing/invalid muted")
    # Registry fixtures omit target for review actions and omit isMajor for
    # all actions; validate these type-only fields when actually present.
    nullable_resolution_ids = []
    for action in audit["actions"]:
        require(isinstance(action, dict) and text(action.get("action")), "invalid audit action")
        require(not ({"error", "errors"} & action.keys()), "error container in audit action")
        # A real registry may group recommendations for different packages and
        # return update + explicit module:null,target:null. Advisory identities
        # remain mandatory; bind these resolution IDs after validating them all.
        nullable_update = (action["action"] == "update" and "module" in action
                           and "target" in action and action["module"] is None
                           and action["target"] is None)
        require(text(action.get("module")) or nullable_update,
                "invalid audit action module or nullable update recommendation")
        if "target" in action:
            require(text(action["target"]) or nullable_update, "invalid audit action target")
        if "isMajor" in action:
            require(type(action["isMajor"]) is bool, "invalid audit action isMajor")
        if "depth" in action:
            require(count(action["depth"]), "invalid audit action depth")
        resolves = action.get("resolves")
        require(isinstance(resolves, list), "invalid audit action resolutions")
        if nullable_update:
            require(bool(resolves), "nullable update recommendation requires nonempty resolutions")
        for resolution in resolves:
            require(isinstance(resolution, dict), "invalid audit action resolution")
            require(not ({"error", "errors"} & resolution.keys()),
                    "error container in audit action resolution")
            require(type(resolution.get("id")) is int and resolution["id"] > 0
                    and text(resolution.get("path")), "invalid resolution id/path")
            require(all(type(resolution.get(flag)) is bool
                        for flag in ("dev", "optional", "bundled")),
                    "invalid resolution dependency flags")
            if nullable_update:
                nullable_resolution_ids.append(resolution["id"])
    advisories = audit.get("advisories")
    require(isinstance(advisories, dict), "missing/invalid advisories")
    metadata = audit.get("metadata")
    require(isinstance(metadata, dict), "missing/invalid metadata")
    counts = metadata.get("vulnerabilities")
    severities = {"info", "low", "moderate", "high", "critical"}
    require(isinstance(counts, dict) and set(counts) == severities,
            "missing/unsupported vulnerability counts")
    require(all(count(value) for value in counts.values()),
            "vulnerability counts must be nonnegative integers")
    require(all(count(metadata.get(name)) for name in
                ("dependencies", "devDependencies", "optionalDependencies", "totalDependencies")),
            "missing/invalid dependency counts")
    represented = set()
    validated_advisory_ids = set()
    for key, advisory in advisories.items():
        require(text(key) and isinstance(advisory, dict), "invalid advisory entry")
        require(text(advisory.get("module_name")), "advisory lacks package")
        severity = advisory.get("severity")
        require(isinstance(severity, str) and severity in severities,
                "advisory has missing/unsupported severity")
        represented.add(severity)
        identifiers = []
        for field in ("github_advisory_id", "url"):
            if field in advisory:
                require(text(advisory[field]), "invalid advisory identifier: " + field)
                identifiers.append(advisory[field])
        if "id" in advisory:
            require(type(advisory["id"]) is int and advisory["id"] > 0,
                    "invalid advisory numeric id")
            identifiers.append(advisory["id"])
        if "cves" in advisory:
            cves = advisory["cves"]
            require(isinstance(cves, list) and all(text(value) for value in cves),
                    "invalid advisory CVEs")
            identifiers.extend(cves)
        require(bool(identifiers), "advisory lacks stable identifier")
        findings = advisory.get("findings")
        require(isinstance(findings, list) and bool(findings), "advisory lacks findings")
        for finding in findings:
            require(isinstance(finding, dict) and text(finding.get("version")),
                    "invalid finding version")
            paths = finding.get("paths")
            require(isinstance(paths, list) and all(text(value) for value in paths),
                    "invalid finding dependency paths")
        if "id" in advisory:
            validated_advisory_ids.add(advisory["id"])
    require(all(identifier in validated_advisory_ids for identifier in nullable_resolution_ids),
            "nullable update resolution lacks complete validated advisory")
    require(represented == {severity for severity, value in counts.items() if value > 0},
            "metadata and represented advisory severities differ")
    require(audit_exit in ("0", "1"), "unexpected pnpm audit exit status")
    require(audit_exit == ("1" if sum(counts.values()) else "0"),
            "pnpm audit exit status disagrees with complete report")


def reject_duplicate_keys(pairs: list[tuple]) -> dict:
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate audit JSON key: " + key)
        result[key] = value
    return result


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--audit", required=True)
    parser.add_argument("--audit-exit", required=True, help="File containing the actual pnpm 9 audit exit status")
    parser.add_argument("--exceptions", required=True)
    args = parser.parse_args()

    try:
        with open(args.audit, "r", encoding="utf-8") as handle:
            audit = json.load(handle, object_pairs_hook=reject_duplicate_keys)
        with open(args.audit_exit, "r", encoding="utf-8") as handle:
            audit_exit = handle.read().strip()
        validate_report(audit, audit_exit)
    except (OSError, ValueError, TypeError) as error:
        sys.stderr.write(f"Invalid pnpm 9 audit report: {error}\n")
        return 1

    # 读取异常清单并建立索引，便于快速匹配包名 + advisory。
    exceptions = parse_exceptions(args.exceptions)
    exception_index = {}
    errors = []

    for exc in exceptions:
        missing = [field for field in REQUIRED_FIELDS if not exc.get(field)]
        if missing:
            errors.append(
                f"Exception missing required fields {missing}: {exc.get('package', '<unknown>')}"
            )
            continue
        exc_severity = normalize_severity(exc.get("severity"))
        exc_package = normalize_package(exc.get("package"))
        exc_advisory = normalize_advisory(exc.get("advisory"))
        exc_date = parse_date(exc.get("expires_on"))
        if exc_date is None:
            errors.append(
                f"Exception has invalid expires_on date: {exc.get('package', '<unknown>')}"
            )
            continue
        if not exc_package or not exc_advisory:
            errors.append("Exception missing package or advisory value")
            continue
        key = (exc_package, exc_advisory)
        if key in exception_index:
            errors.append(
                f"Duplicate exception for {exc_package} advisory {exc.get('advisory')}"
            )
            continue
        exception_index[key] = {
            "raw": exc,
            "severity": exc_severity,
            "expires_on": exc_date,
        }

    today = date.today()
    missing_exceptions = []
    expired_exceptions = []

    # 去重处理：同一包名 + advisory 可能在不同字段重复出现。
    seen = set()
    for name, severity, advisory_id, title in iter_vulns(audit):
        sev = normalize_severity(severity)
        if sev not in HIGH_SEVERITIES or not name:
            continue
        advisory_key = normalize_advisory(advisory_id)
        if not advisory_key:
            errors.append(
                f"High/Critical vulnerability missing advisory id: {name} ({sev})"
            )
            continue
        key = (normalize_package(name), advisory_key)
        if key in seen:
            continue
        seen.add(key)
        exc = exception_index.get(key)
        if exc is None:
            missing_exceptions.append((name, sev, advisory_id, title))
            continue
        if exc["severity"] and exc["severity"] != sev:
            errors.append(
                "Exception severity mismatch: "
                f"{name} ({advisory_id}) expected {sev}, got {exc['severity']}"
            )
        if exc["expires_on"] and exc["expires_on"] < today:
            expired_exceptions.append(
                (name, sev, advisory_id, exc["expires_on"].isoformat())
            )

    if missing_exceptions:
        errors.append("High/Critical vulnerabilities missing exceptions:")
        for name, sev, advisory_id, title in missing_exceptions:
            label = f"{name} ({sev})"
            if advisory_id:
                label = f"{label} [{advisory_id}]"
            if title:
                label = f"{label}: {title}"
            errors.append(f"- {label}")

    if expired_exceptions:
        errors.append("Exceptions expired:")
        for name, sev, advisory_id, expires_on in expired_exceptions:
            errors.append(
                f"- {name} ({sev}) [{advisory_id}] expired on {expires_on}"
            )

    if errors:
        sys.stderr.write("\n".join(errors) + "\n")
        return 1

    print("Audit exceptions validated.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())