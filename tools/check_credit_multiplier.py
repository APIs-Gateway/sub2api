#!/usr/bin/env python3
"""CI 闸门：充值倍率只能在白名单文件里被读取。

背景：codex 站正在从「额度」记账改成直接以人民币记账（见 backend/internal/service/credit_unit.go）。
BALANCE_RECHARGE_MULTIPLIER（Go 里叫 BalanceRechargeMultiplier）是旧口径的 ×13 / ÷13 因子，
散在业务代码里随手读它，换算就会漏改、错 13 倍。所以只允许白名单里的文件出现它，
新代码要换算必须走 credit_unit.go。

匹配的是「等价读取方式」，不只是标识符本身：不区分大小写、下划线可有可无的 balance_recharge_mult，
覆盖 BalanceRechargeMultiplier、SettingBalanceRechargeMult、normalizeBalanceRechargeMultiplier、
defaultBalanceRechargeMultiplier、"BALANCE_RECHARGE_MULTIPLIER"、JSON 键 balance_recharge_multiplier 等。

范围：backend/ 下的非测试 Go 文件（*_test.go 不检查）。前端的 balance_recharge_multiplier 由前端那组 PR
（R5a）收口后再加闸门，这里不管。

用法（仓库根目录）：
    python3 tools/check_credit_multiplier.py
白名单：.github/credit-multiplier-allowlist.txt，每行一个相对仓库根的路径，# 开头是注释。
白名单里不再出现该符号的文件只提示「可以删掉」，不报错；删掉后闸门会更严，这是预期的方向。
"""
import argparse
import os
import re
import sys

PATTERN = re.compile(r"balance_?recharge_?mult", re.IGNORECASE)
SCAN_ROOTS = ("backend",)
SKIP_DIRS = {".git", "node_modules", "vendor"}
DEFAULT_ALLOWLIST = os.path.join(".github", "credit-multiplier-allowlist.txt")


def iter_go_files(root):
    """产出 (相对 root 的 posix 路径, 绝对路径)：scan roots 下所有非测试 Go 文件。"""
    for scan_root in SCAN_ROOTS:
        base = os.path.join(root, scan_root)
        for dirpath, dirnames, filenames in os.walk(base):
            dirnames[:] = sorted(d for d in dirnames if d not in SKIP_DIRS)
            for name in sorted(filenames):
                if not name.endswith(".go") or name.endswith("_test.go"):
                    continue
                full = os.path.join(dirpath, name)
                rel = os.path.relpath(full, root).replace(os.sep, "/")
                yield rel, full


def find_hits(root):
    """返回 {相对路径: [(行号, 行文本), ...]}，只含命中的文件。"""
    hits = {}
    for rel, full in iter_go_files(root):
        with open(full, "r", encoding="utf-8", errors="replace") as handle:
            for lineno, line in enumerate(handle, 1):
                if PATTERN.search(line):
                    hits.setdefault(rel, []).append((lineno, line.rstrip("\n")))
    return hits


def load_allowlist(path):
    allowed = set()
    with open(path, "r", encoding="utf-8") as handle:
        for raw in handle:
            line = raw.split("#", 1)[0].strip()
            if line:
                allowed.add(line.replace("\\", "/"))
    return allowed


def check(root, allowlist_path):
    """返回 (violations, stale)：violations 是不在白名单里的命中，stale 是白名单里已不再命中的路径。"""
    hits = find_hits(root)
    allowed = load_allowlist(allowlist_path)
    violations = {rel: lines for rel, lines in hits.items() if rel not in allowed}
    stale = sorted(path for path in allowed if path not in hits)
    return violations, stale


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    parser.add_argument("--root", default=os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    parser.add_argument("--allowlist", default=None, help="白名单文件，默认 <root>/" + DEFAULT_ALLOWLIST)
    args = parser.parse_args(argv)

    allowlist = args.allowlist or os.path.join(args.root, DEFAULT_ALLOWLIST)
    if not os.path.isfile(allowlist):
        print("找不到白名单文件: " + allowlist, file=sys.stderr)
        return 2

    violations, stale = check(args.root, allowlist)

    for path in stale:
        print("提示：白名单里的 %s 已不再出现充值倍率，可以从白名单删掉。" % path)

    if violations:
        print("充值倍率（BALANCE_RECHARGE_MULTIPLIER / BalanceRechargeMultiplier）只能出现在白名单文件里，下面这些不在白名单：", file=sys.stderr)
        for rel in sorted(violations):
            for lineno, text in violations[rel]:
                print("  %s:%d: %s" % (rel, lineno, text.strip()), file=sys.stderr)
        print(
            "\n额度与人民币之间的换算请走 backend/internal/service/credit_unit.go，不要直接读充值倍率。\n"
            "确实需要新增读取点时，在 %s 里登记该文件，并在 PR 里说明为什么不能走 credit_unit.go。" % DEFAULT_ALLOWLIST,
            file=sys.stderr,
        )
        return 1

    print("充值倍率闸门通过：%d 个文件出现该符号，均在白名单内。" % len(find_hits(args.root)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
