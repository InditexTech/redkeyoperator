#!/usr/bin/env python3

from __future__ import annotations

import argparse
import json
from collections import Counter, OrderedDict, defaultdict
from pathlib import Path
from typing import Any


FAILURE_STATES = {"failed", "aborted", "panicked", "interrupted", "timedout"}


def escape_markdown(value: str) -> str:
    return value.replace("|", "\\|").replace("\n", "<br>")


def format_seconds(seconds: float | int | None) -> str:
    if seconds is None:
        return "-"

    total_seconds = float(seconds)
    if total_seconds < 0:
        total_seconds = 0.0

    if total_seconds < 1:
        return f"{total_seconds * 1000:.0f}ms"
    if total_seconds < 60:
        return f"{total_seconds:.2f}s"

    minutes, remainder = divmod(total_seconds, 60)
    hours, minutes = divmod(minutes, 60)
    if hours >= 1:
        return f"{int(hours)}h {int(minutes)}m {remainder:.1f}s"
    return f"{int(minutes)}m {remainder:.1f}s"


def format_nanoseconds(value: Any) -> str:
    if value is None:
        return "-"

    try:
        seconds = float(value) / 1_000_000_000
    except (TypeError, ValueError):
        return "-"

    return format_seconds(seconds)


def read_json_lines(path: Path) -> list[dict[str, Any]]:
    records: list[dict[str, Any]] = []
    if not path.exists():
        return records

    for line in path.read_text(encoding="utf-8").splitlines():
        stripped = line.strip()
        if not stripped:
            continue
        try:
            records.append(json.loads(stripped))
        except json.JSONDecodeError:
            continue
    return records


def read_json(path: Path) -> Any:
    if not path.exists():
        return None
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError:
        return None


def parse_total_coverage(path: Path | None) -> str:
    if path is None or not path.exists():
        return "not available"

    for line in path.read_text(encoding="utf-8").splitlines():
        stripped = line.strip()
        if stripped.startswith("total:"):
            return stripped.split()[-1]
    return "not available"


def summarize_output(lines: list[str]) -> str:
    if not lines:
        return "-"

    ignored_prefixes = (
        "=== RUN",
        "=== PAUSE",
        "=== CONT",
        "--- PASS",
        "--- FAIL",
        "--- SKIP",
        "PASS",
        "FAIL",
        "ok\t",
        "ok ",
        "?\t",
    )

    filtered = [
        line.strip()
        for line in lines
        if line.strip() and not line.strip().startswith(ignored_prefixes)
    ]

    if not filtered:
        filtered = [line.strip() for line in lines if line.strip()]

    return "<br>".join(escape_markdown(line) for line in filtered[-3:]) if filtered else "-"


def pretty_state(state: str) -> str:
    mapping = {
        "passed": "PASS",
        "failed": "FAIL",
        "skipped": "SKIP",
        "pending": "PENDING",
        "aborted": "ABORTED",
        "panicked": "PANIC",
        "interrupted": "INTERRUPTED",
        "timedout": "TIMEOUT",
    }
    return mapping.get(state, state.upper())


def normalize_unit_state(action: str | None) -> str:
    mapping = {
        "pass": "passed",
        "fail": "failed",
        "skip": "skipped",
    }
    return mapping.get((action or "").lower(), (action or "unknown").lower())


def render_unit_report(input_path: Path, coverage_path: Path | None, title: str) -> str:
    events = read_json_lines(input_path)
    coverage = parse_total_coverage(coverage_path)

    tests: OrderedDict[tuple[str, str], dict[str, Any]] = OrderedDict()
    test_output: defaultdict[tuple[str, str], list[str]] = defaultdict(list)
    package_output: defaultdict[str, list[str]] = defaultdict(list)
    package_failures: set[str] = set()

    for event in events:
        package = event.get("Package", "")
        test_name = event.get("Test")
        action = event.get("Action")
        output = event.get("Output")

        if output:
            if test_name:
                test_output[(package, test_name)].append(output.rstrip())
            elif package:
                package_output[package].append(output.rstrip())

        if test_name and action in {"pass", "fail", "skip"}:
            tests[(package, test_name)] = {
                "package": package,
                "name": test_name,
                "state": normalize_unit_state(action),
                "duration": event.get("Elapsed"),
            }
        elif not test_name and action == "fail" and package:
            package_failures.add(package)

    package_level_rows: list[dict[str, Any]] = []
    failed_packages_with_tests = {
        row["package"] for row in tests.values() if row["state"] == "failed"
    }
    for package in sorted(package_failures):
        if package in failed_packages_with_tests:
            continue
        package_level_rows.append(
            {
                "package": package,
                "name": "package setup",
                "state": "failed",
                "duration": None,
                "details": summarize_output(package_output[package]),
            }
        )

    counts = Counter(row["state"] for row in tests.values())
    total = len(tests) + len(package_level_rows)
    failed = counts.get("failed", 0) + len(package_level_rows)

    lines = [
        f"### {title}",
        "",
        "| Metric | Value |",
        "| --- | --- |",
        f"| Total tests | {total} |",
        f"| Passed | {counts.get('passed', 0)} |",
        f"| Failed | {failed} |",
        f"| Skipped | {counts.get('skipped', 0)} |",
        f"| Coverage | {coverage} |",
        "",
    ]

    failed_rows = []
    for key, row in tests.items():
        if row["state"] != "failed":
            continue
        failed_rows.append(
            {
                **row,
                "details": summarize_output(test_output[key]),
            }
        )

    failed_rows.extend(package_level_rows)

    if not failed_rows:
        lines.append("_No failed unit tests._")
        return "\n".join(lines).strip() + "\n"

    lines.extend(
        [
            "Failed unit tests:",
            "",
            "| Test | Result | Duration | Details |",
            "| --- | --- | --- | --- |",
        ]
    )

    for row in failed_rows:
        test_label = f"{row['package']}::{row['name']}" if row["package"] else row["name"]
        lines.append(
            "| "
            f"{escape_markdown(test_label)} | {pretty_state(row['state'])} | {format_seconds(row['duration'])} | {row['details']} |"
        )

    return "\n".join(lines).strip() + "\n"


def full_spec_name(spec: dict[str, Any]) -> str:
    containers = spec.get("ContainerHierarchyTexts") or []
    leaf_text = spec.get("LeafNodeText") or spec.get("LeafNodeType") or "suite node"
    return " ".join([*containers, leaf_text]).strip()


def normalize_ginkgo_state(state: Any) -> str:
    return str(state or "unknown").strip().lower()


def render_ginkgo_report(input_path: Path, title: str) -> str:
    raw_report = read_json(input_path)
    reports: list[dict[str, Any]]

    if raw_report is None:
        reports = []
    elif isinstance(raw_report, list):
        reports = [report for report in raw_report if isinstance(report, dict)]
    elif isinstance(raw_report, dict):
        reports = [raw_report]
    else:
        reports = []

    spec_rows: list[dict[str, Any]] = []
    total_runtime = 0

    for report in reports:
        total_runtime += int(report.get("RunTime") or 0)
        for spec in report.get("SpecReports") or []:
            if not isinstance(spec, dict):
                continue
            state = normalize_ginkgo_state(spec.get("State"))
            failure = spec.get("Failure") or {}
            details = failure.get("Message") or "-"
            spec_rows.append(
                {
                    "name": full_spec_name(spec),
                    "state": state,
                    "duration": spec.get("RunTime"),
                    "details": escape_markdown(str(details).strip()) if details and details != "-" else "-",
                }
            )

    counts = Counter(row["state"] for row in spec_rows)
    failed = sum(count for state, count in counts.items() if state in FAILURE_STATES)

    lines = [
        f"### {title}",
        "",
        "| Metric | Value |",
        "| --- | --- |",
        f"| Total specs | {len(spec_rows)} |",
        f"| Passed | {counts.get('passed', 0)} |",
        f"| Failed | {failed} |",
        f"| Skipped | {counts.get('skipped', 0)} |",
        f"| Pending | {counts.get('pending', 0)} |",
        f"| Runtime | {format_nanoseconds(total_runtime)} |",
        "",
    ]

    if not spec_rows:
        lines.append("_No test report was generated._")
        return "\n".join(lines).strip() + "\n"

    lines.extend(
        [
            "| Test | Result | Duration | Details |",
            "| --- | --- | --- | --- |",
        ]
    )

    for row in spec_rows:
        lines.append(
            "| "
            f"{escape_markdown(row['name'])} | {pretty_state(row['state'])} | {format_nanoseconds(row['duration'])} | {row['details']} |"
        )

    return "\n".join(lines).strip() + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description="Render markdown summaries for CI test reports.")
    subparsers = parser.add_subparsers(dest="report_type", required=True)

    unit_parser = subparsers.add_parser("unit", help="Render a unit-test summary from go test JSON output.")
    unit_parser.add_argument("--input", required=True, type=Path)
    unit_parser.add_argument("--coverage", type=Path)
    unit_parser.add_argument("--title", required=True)
    unit_parser.add_argument("--output", required=True, type=Path)

    ginkgo_parser = subparsers.add_parser("ginkgo", help="Render a summary from a Ginkgo JSON report.")
    ginkgo_parser.add_argument("--input", required=True, type=Path)
    ginkgo_parser.add_argument("--title", required=True)
    ginkgo_parser.add_argument("--output", required=True, type=Path)

    args = parser.parse_args()

    if args.report_type == "unit":
        markdown = render_unit_report(args.input, args.coverage, args.title)
    else:
        markdown = render_ginkgo_report(args.input, args.title)

    args.output.write_text(markdown, encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())