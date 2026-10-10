#!/usr/bin/env python3
"""Deterministic helpers for the karpathy-audit skill."""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
from copy import deepcopy
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


CONFIG_NAME = ".karpathy.json"
DOC_SUFFIXES = {".md", ".mdx", ".txt", ".rst"}
INDEX_NAMES = {"index.md", "readme.md"}
IGNORED_DIRS = {
    ".git",
    ".hg",
    ".svn",
    ".next",
    ".venv",
    "__pycache__",
    "build",
    "coverage",
    "dist",
    "node_modules",
    "target",
    "vendor",
}

DEFAULT_AUDIT_CONFIG: dict[str, Any] = {
    "staleDocs": True,
    "indexChecks": True,
    "docPaths": ["README.md", "docs", "knowledge", "specs", "roadmap.md", "TODO.md"],
    "indexThreshold": 5,
}

RECOMMENDED_AUDIT_CONFIG: dict[str, Any] = {
    "staleDocs": True,
    "indexChecks": True,
}

CHECKS = [
    {
        "display_id": "D1",
        "id": "stale-docs",
        "key": "staleDocs",
        "title": "Stale docs",
        "summary": "Scan docs for TODOs, temporal claims, past plan dates, and broken local doc links.",
    },
    {
        "display_id": "D2",
        "id": "doc-indexes",
        "key": "indexChecks",
        "title": "Doc indexes",
        "summary": "Check large doc folders for README.md/index.md and direct child doc listings.",
    },
]

STALE_PATTERNS = [
    ("todo marker", re.compile(r"\b(TODO|FIXME|TBD)\b")),
    (
        "temporal marker",
        re.compile(
            r"\b(currently|for now|this week|next week|current sprint|in flight|temporarily|soon)\b",
            re.IGNORECASE,
        ),
    ),
    ("phase marker", re.compile(r"\bphase\s+\d+\b", re.IGNORECASE)),
]

DATE_PATTERN = re.compile(r"\b(20\d\d-\d\d-\d\d)\b")
PLAN_WORD_PATTERN = re.compile(
    r"\b(target|deadline|due|eta|planned|plan|ship|launch|by|before|after|current|roadmap)\b",
    re.IGNORECASE,
)
MARKDOWN_LINK_PATTERN = re.compile(r"(?<!!)\[[^\]]+\]\(([^)]+)\)")


@dataclass
class Issue:
    check: str
    severity: str
    path: str
    message: str
    line: int | None = None
    excerpt: str | None = None

    def to_dict(self) -> dict[str, Any]:
        data: dict[str, Any] = {
            "check": self.check,
            "severity": self.severity,
            "path": self.path,
            "message": self.message,
        }
        if self.line is not None:
            data["line"] = self.line
        if self.excerpt:
            data["excerpt"] = self.excerpt
        return data


def read_text(path: Path) -> str:
    return path.read_text(encoding="utf-8", errors="replace")


def rel(repo: Path, path: Path) -> str:
    try:
        return path.resolve().relative_to(repo.resolve()).as_posix()
    except ValueError:
        return path.as_posix()


def config_path(repo: Path) -> Path:
    return repo / CONFIG_NAME


def read_raw_config(repo: Path) -> dict[str, Any]:
    path = config_path(repo)
    if not path.exists():
        return {}
    try:
        data = json.loads(read_text(path))
    except json.JSONDecodeError as exc:
        return {"_error": f"{CONFIG_NAME} is invalid JSON: {exc}"}
    return data if isinstance(data, dict) else {"_error": f"{CONFIG_NAME} must contain a JSON object"}


def effective_audit_config(raw: dict[str, Any]) -> dict[str, Any]:
    audit = raw.get("audit") if isinstance(raw.get("audit"), dict) else {}
    config = deepcopy(DEFAULT_AUDIT_CONFIG)
    for key in DEFAULT_AUDIT_CONFIG:
        if key in audit:
            config[key] = audit[key]
    config["staleDocs"] = bool(config.get("staleDocs"))
    config["indexChecks"] = bool(config.get("indexChecks"))
    try:
        config["indexThreshold"] = max(1, int(config.get("indexThreshold", DEFAULT_AUDIT_CONFIG["indexThreshold"])))
    except (TypeError, ValueError):
        config["indexThreshold"] = DEFAULT_AUDIT_CONFIG["indexThreshold"]
    if not isinstance(config.get("docPaths"), list):
        config["docPaths"] = list(DEFAULT_AUDIT_CONFIG["docPaths"])
    config["docPaths"] = [str(entry) for entry in config["docPaths"] if str(entry).strip()]
    return config


def write_audit_config(repo: Path, audit_config: dict[str, Any]) -> None:
    raw = read_raw_config(repo)
    raw.pop("_error", None)
    raw["audit"] = audit_config
    config_path(repo).write_text(json.dumps(raw, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def selector_to_check(selector: str) -> dict[str, str]:
    normalized = selector.strip().lower()
    for check in CHECKS:
        aliases = {
            check["display_id"].lower(),
            check["id"].lower(),
            check["key"].lower(),
            check["title"].lower().replace(" ", "-"),
        }
        if normalized in aliases:
            return check
    raise ValueError(f"Unknown audit setup row or check: {selector}")


def expand_selectors(values: list[str] | None) -> list[str]:
    selectors: list[str] = []
    for value in values or []:
        selectors.extend(part.strip() for part in value.split(",") if part.strip())
    return selectors


def setup(repo: Path, args: argparse.Namespace) -> dict[str, Any]:
    raw = read_raw_config(repo)
    if raw.get("_error"):
        return {
            "command": "setup",
            "status": "error",
            "configPath": rel(repo, config_path(repo)),
            "error": raw["_error"],
            "checks": setup_rows(effective_audit_config({})),
            "actions": [],
        }

    current = effective_audit_config(raw)
    actions: list[str] = []
    mutated = False

    if args.yes:
        current = {**current, **RECOMMENDED_AUDIT_CONFIG}
        actions.append("Saved recommended audit docs checks: D1 stale-docs on, D2 doc-indexes on")
        mutated = True

    if args.reset:
        current = deepcopy(DEFAULT_AUDIT_CONFIG)
        actions.append("Reset audit setup to opinionated defaults")
        mutated = True

    toggle_selectors = expand_selectors(args.toggle) + expand_selectors(args.positionals)
    for selector in toggle_selectors:
        check = selector_to_check(selector)
        current[check["key"]] = not bool(current.get(check["key"]))
        state = "on" if current[check["key"]] else "off"
        actions.append(f"Toggled {check['display_id']} {check['id']} {state}")
        mutated = True

    for selector in expand_selectors(args.enable):
        check = selector_to_check(selector)
        current[check["key"]] = True
        actions.append(f"Enabled {check['display_id']} {check['id']}")
        mutated = True

    for selector in expand_selectors(args.disable):
        check = selector_to_check(selector)
        current[check["key"]] = False
        actions.append(f"Disabled {check['display_id']} {check['id']}")
        mutated = True

    if mutated:
        write_audit_config(repo, current)

    return {
        "command": "setup",
        "status": "configured" if mutated else "reported",
        "configPath": rel(repo, config_path(repo)) if config_path(repo).exists() else None,
        "checks": setup_rows(current),
        "docPaths": current["docPaths"],
        "indexThreshold": current["indexThreshold"],
        "actions": actions,
    }


def setup_rows(audit_config: dict[str, Any]) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = [
        {
            "display_id": "A1",
            "id": "instruction-coverage",
            "state": "on",
            "title": "Instruction coverage",
            "summary": "Check whether CLAUDE.md, AGENTS.md, or rules cover the four principles.",
            "mutable": False,
        },
        {
            "display_id": "A2",
            "id": "instruction-quality",
            "state": "on",
            "title": "Instruction quality",
            "summary": "Check for bloat, staleness, ambiguity, verification gaps, and contradictions.",
            "mutable": False,
        },
    ]
    for check in CHECKS:
        rows.append(
            {
                "display_id": check["display_id"],
                "id": check["id"],
                "state": "on" if audit_config.get(check["key"]) else "off",
                "title": check["title"],
                "summary": check["summary"],
                "mutable": True,
            }
        )
    return rows


def render_setup(report: dict[str, Any]) -> str:
    lines = [
        "Karpathy audit setup",
        f"Status: {report['status']}",
        f"Config: {report['configPath'] or 'not saved; showing opinionated defaults'}",
        "",
        "Audit checks:",
        "",
        "A. Agent instruction checks:",
    ]
    for row in report["checks"]:
        if not row["display_id"].startswith("A"):
            continue
        lines.append(format_setup_row(row))
    lines.extend(["", "D. Documentation checks:"])
    for row in report["checks"]:
        if not row["display_id"].startswith("D"):
            continue
        lines.append(format_setup_row(row))

    if report.get("docPaths"):
        lines.extend(
            [
                "",
                "Docs scope:",
                f"- Paths: {', '.join(report['docPaths'])}",
                f"- Large-folder threshold: {report['indexThreshold']} direct doc files",
            ]
        )

    if report.get("actions"):
        lines.extend(["", "Setup actions:"])
        lines.extend(f"- {action}" for action in report["actions"])

    lines.extend(
        [
            "",
            "Controls:",
            "- Toggle a row: /karpathy setup D1",
            "- Explicit toggle flag: /karpathy setup --toggle D1",
            "- Enable or disable: /karpathy setup --enable stale-docs | /karpathy setup --disable doc-indexes",
            "- Save current recommended defaults: /karpathy setup --yes",
            "- Reset to opinionated defaults: /karpathy setup --reset",
            "- Alias: /karpathy configure accepts the same controls.",
        ]
    )
    return "\n".join(lines)


def format_setup_row(row: dict[str, Any]) -> str:
    return f"{row['display_id']}  {row['state']:<3}  {row['id']:<22} {row['title']} | {row['summary']}"


def is_doc_file(path: Path) -> bool:
    return path.is_file() and path.suffix.lower() in DOC_SUFFIXES


def is_ignored(path: Path) -> bool:
    return any(part in IGNORED_DIRS for part in path.parts)


def configured_doc_roots(repo: Path, audit_config: dict[str, Any]) -> list[Path]:
    roots: list[Path] = []
    for entry in audit_config.get("docPaths", []):
        path = (repo / entry).resolve()
        if path.exists() and not is_ignored(path):
            roots.append(path)
    return sorted(set(roots))


def iter_doc_files(paths: list[Path]) -> list[Path]:
    files: list[Path] = []
    for path in paths:
        if is_doc_file(path) and not is_ignored(path):
            files.append(path)
        elif path.is_dir():
            for candidate in path.rglob("*"):
                if is_doc_file(candidate) and not is_ignored(candidate):
                    files.append(candidate)
    return sorted(set(files))


def scan_stale_docs(repo: Path, roots: list[Path]) -> list[Issue]:
    issues: list[Issue] = []
    today = datetime.now(timezone.utc).date()
    for path in iter_doc_files(roots):
        lines = read_text(path).splitlines()
        per_file = 0
        in_fence = False
        for line_number, line in enumerate(lines, start=1):
            if line.lstrip().startswith("```"):
                in_fence = not in_fence
                continue
            if in_fence:
                continue
            if per_file >= 5:
                break
            stripped = line.strip()
            if not stripped or stripped.startswith("timestamp:") or stripped.startswith("source_commit:"):
                continue
            if "stale notes like" in stripped.lower() or "stale docs checks" in stripped.lower():
                continue
            for name, pattern in STALE_PATTERNS:
                match = pattern.search(stripped)
                if not match:
                    continue
                issues.append(
                    Issue(
                        "stale-docs",
                        "warning",
                        rel(repo, path),
                        f"possible stale {name}: {match.group(0)}",
                        line_number,
                        stripped[:180],
                    )
                )
                per_file += 1
                break
            if per_file >= 5:
                break
            if PLAN_WORD_PATTERN.search(stripped):
                for date_match in DATE_PATTERN.finditer(stripped):
                    try:
                        date = datetime.strptime(date_match.group(1), "%Y-%m-%d").date()
                    except ValueError:
                        continue
                    age_days = (today - date).days
                    if age_days > 90:
                        issues.append(
                            Issue(
                                "stale-docs",
                                "warning",
                                rel(repo, path),
                                f"past dated planning claim: {date_match.group(1)} is {age_days} days old",
                                line_number,
                                stripped[:180],
                            )
                        )
                        per_file += 1
                        break
        issues.extend(scan_broken_doc_links(repo, path))
    return issues


def scan_broken_doc_links(repo: Path, path: Path) -> list[Issue]:
    issues: list[Issue] = []
    in_fence = False
    for line_number, line in enumerate(read_text(path).splitlines(), start=1):
        if line.lstrip().startswith("```"):
            in_fence = not in_fence
            continue
        if in_fence:
            continue
        for match in MARKDOWN_LINK_PATTERN.finditer(line):
            target = match.group(1).strip()
            if not target or target.startswith("#") or is_external_link(target):
                continue
            target = target.split("#", 1)[0].split("?", 1)[0]
            if not target:
                continue
            resolved = (path.parent / target).resolve()
            if target.endswith("/"):
                exists = resolved.is_dir() or (resolved / "index.md").exists() or (resolved / "README.md").exists()
            else:
                exists = resolved.exists()
            if not exists:
                issues.append(
                    Issue(
                        "stale-docs",
                        "critical",
                        rel(repo, path),
                        f"local doc link target does not exist: {target}",
                        line_number,
                        line.strip()[:180],
                    )
                )
    return issues


def is_external_link(target: str) -> bool:
    return bool(re.match(r"^[a-z][a-z0-9+.-]*:", target, re.IGNORECASE)) or target.startswith("/")


def scan_index_checks(repo: Path, roots: list[Path], threshold: int) -> list[Issue]:
    issues: list[Issue] = []
    for directory in iter_doc_dirs(roots):
        direct_docs = [
            child
            for child in sorted(directory.iterdir())
            if is_doc_file(child) and child.name.lower() not in INDEX_NAMES
        ]
        if len(direct_docs) < threshold:
            continue
        index = directory / "index.md"
        readme = directory / "README.md"
        index_file = index if index.exists() else readme if readme.exists() else None
        if index_file is None:
            issues.append(
                Issue(
                    "doc-indexes",
                    "warning",
                    rel(repo, directory),
                    f"{len(direct_docs)} direct doc files but no README.md or index.md",
                )
            )
            continue
        index_text = read_text(index_file).lower()
        missing = [
            child.name
            for child in direct_docs
            if child.name.lower() not in index_text and child.stem.lower() not in index_text
        ]
        if missing:
            preview = ", ".join(missing[:8])
            extra = "" if len(missing) <= 8 else f", and {len(missing) - 8} more"
            issues.append(
                Issue(
                    "doc-indexes",
                    "warning",
                    rel(repo, index_file),
                    f"index does not mention {len(missing)} direct doc file(s): {preview}{extra}",
                )
            )
    return issues


def iter_doc_dirs(roots: list[Path]) -> list[Path]:
    dirs: set[Path] = set()
    for root in roots:
        if root.is_dir() and not is_ignored(root):
            dirs.add(root)
            for dirpath, dirnames, _filenames in os.walk(root):
                dirnames[:] = [dirname for dirname in dirnames if dirname not in IGNORED_DIRS]
                dirs.add(Path(dirpath))
        elif root.is_file():
            dirs.add(root.parent)
    return sorted(dirs)


def docs_check(repo: Path, args: argparse.Namespace) -> dict[str, Any]:
    raw = read_raw_config(repo)
    if raw.get("_error"):
        return {
            "command": "docs-check",
            "status": "error",
            "configPath": rel(repo, config_path(repo)),
            "error": raw["_error"],
            "checks": [],
            "issues": [],
            "clean": [],
        }

    audit_config = effective_audit_config(raw)
    roots = configured_doc_roots(repo, audit_config)
    checks = [
        {"id": "stale-docs", "enabled": bool(audit_config["staleDocs"])},
        {"id": "doc-indexes", "enabled": bool(audit_config["indexChecks"])},
    ]
    issues: list[Issue] = []
    clean: list[str] = []

    if audit_config["staleDocs"]:
        stale = scan_stale_docs(repo, roots)
        issues.extend(stale)
        if not stale:
            clean.append("stale-docs found no stale markers or broken local doc links")
    else:
        clean.append("stale-docs disabled")

    if audit_config["indexChecks"]:
        index_issues = scan_index_checks(repo, roots, audit_config["indexThreshold"])
        issues.extend(index_issues)
        if not index_issues:
            clean.append("doc-indexes found no large unindexed doc folders")
    else:
        clean.append("doc-indexes disabled")

    status = "reported" if any(check["enabled"] for check in checks) else "skipped"
    return {
        "command": "docs-check",
        "status": status,
        "configPath": rel(repo, config_path(repo)) if config_path(repo).exists() else None,
        "checks": checks,
        "docPaths": [rel(repo, path) for path in roots],
        "indexThreshold": audit_config["indexThreshold"],
        "issues": [issue.to_dict() for issue in issues],
        "clean": clean,
        "sourceCommit": git_rev_parse(repo),
    }


def git_rev_parse(repo: Path) -> str:
    try:
        result = subprocess.run(
            ["git", "rev-parse", "--short", "HEAD"],
            cwd=repo,
            text=True,
            capture_output=True,
            check=True,
        )
        return result.stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        return ""


def render_docs_check(report: dict[str, Any]) -> str:
    lines = [
        "Karpathy audit docs checks",
        f"Status: {report['status']}",
        f"Config: {report['configPath'] or 'not saved'}",
    ]
    enabled = [check["id"] for check in report["checks"] if check["enabled"]]
    lines.append(f"Enabled: {', '.join(enabled) if enabled else 'none'}")
    if report.get("docPaths"):
        lines.append(f"Docs scope: {', '.join(report['docPaths'])}")
    if report.get("issues"):
        lines.extend(["", "Issues:"])
        for issue in report["issues"]:
            location = issue["path"]
            if issue.get("line"):
                location += f":{issue['line']}"
            lines.append(f"- [{issue['severity']}] {issue['check']} {location} - {issue['message']}")
    if report.get("clean"):
        lines.extend(["", "Clean checks:"])
        lines.extend(f"- {entry}" for entry in report["clean"])
    return "\n".join(lines)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    setup_parser = sub.add_parser("setup", help="Show or update Karpathy audit setup")
    add_common(setup_parser)
    setup_parser.add_argument("positionals", nargs="*", help="Row IDs or check IDs to toggle")
    setup_parser.add_argument("--toggle", nargs="*", default=[], help="Row IDs or check IDs to toggle")
    setup_parser.add_argument("--enable", nargs="*", default=[], help="Row IDs or check IDs to enable")
    setup_parser.add_argument("--disable", nargs="*", default=[], help="Row IDs or check IDs to disable")
    setup_parser.add_argument("--yes", action="store_true", help="Save recommended audit docs checks")
    setup_parser.add_argument("--reset", action="store_true", help="Reset to opinionated defaults")
    setup_parser.add_argument("--json", action="store_true", help="Print JSON")

    docs_parser = sub.add_parser("docs-check", help="Run configured optional documentation checks")
    add_common(docs_parser)
    docs_parser.add_argument("--json", action="store_true", help="Print JSON")

    return parser


def add_common(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--repo", default=".", help="Repository root")


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    repo = Path(args.repo).resolve()
    try:
        if args.command == "setup":
            report = setup(repo, args)
            print(json.dumps(report, indent=2, sort_keys=True) if args.json else render_setup(report))
            return 1 if report.get("status") == "error" else 0
        if args.command == "docs-check":
            report = docs_check(repo, args)
            print(json.dumps(report, indent=2, sort_keys=True) if args.json else render_docs_check(report))
            return 1 if report.get("status") == "error" else 0
    except ValueError as exc:
        parser.error(str(exc))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
