#!/usr/bin/env python3
"""Convert Ginkgo JSON test results to Markdown format."""

import json
import sys
import pathlib

def escape_md(text: str) -> str:
    """Escape special characters for markdown."""
    return text.replace("*", "\\*").replace("_", "\\_").replace("`", "\\`")


def format_runtime(nanoseconds: int) -> str:
    """Convert nanoseconds to human-readable format."""
    seconds = nanoseconds / 1e9
    if seconds < 60:
        return f"{seconds:.2f}s"
    minutes = int(seconds // 60)
    secs = seconds % 60
    return f"{minutes}m {secs:.2f}s"


def generate_md_report(data: dict) -> str:
    """Generate markdown report from Ginkgo JSON data."""
    suite = data[0]
    lines = []

    # Header
    lines.append("# " + suite["SuiteDescription"] + " Results")
    lines.append("")
    lines.append("**Date:** " + suite["StartTime"][:10])
    lines.append("")

    # Summary
    specs = suite["SpecReports"]
    passed = sum(1 for s in specs if s.get("State") == "passed" and s.get("LeafNodeType") == "It")
    failed = sum(1 for s in specs if s.get("State") == "failed")
    total = suite["PreRunStats"]["TotalSpecs"]

    status = "PASS" if suite["SuiteSucceeded"] else "FAIL"
    lines.append(f"## {status}: {suite['SuiteDescription']} ({passed}/{total} passed)")
    lines.append("")

    for spec in specs:
        node_type = spec.get("LeafNodeType", "Unknown")
        state = spec.get("State", "unknown")
        text = spec.get("LeafNodeText", "")
        hierarchy = spec.get("ContainerHierarchyTexts") or []
        runtime = spec.get("RunTime", 0)
        location = spec.get("LeafNodeLocation", {})
        file_name = location.get("FileName", "")
        line_number = location.get("LineNumber", "")
        failure = spec.get("Failure")
        captured_output = spec.get("CapturedGinkgoWriterOutput", "")

        # Skip BeforeSuite/AfterSuite
        if node_type != "It":
            continue

        # Build test name from hierarchy + text
        full_name = " > ".join(hierarchy + [text]) if hierarchy else text
        status = "PASS" if state in ("passed", "skipped") else "FAIL"

        if status == "PASS": # We do not want PASS tests
            continue

        lines.append(f"### {status}: {escape_md(full_name)}")
        lines.append("")

        # Add test location and runtime
        if file_name:
            short_file = file_name.split("redkeyoperator/")[-1]
            lines.append(f"- **Test:** [{short_file}:{line_number}]({file_name}#L{line_number})")
        lines.append(f"- **Runtime:** {format_runtime(runtime)}")
        lines.append("")

        # Add failure details
        if failure:
            lines.append("#### Failure")
            lines.append("")
            fail_loc = failure.get("Location", {})
            if fail_loc:
                fail_file = fail_loc.get("FileName", "")
                fail_line = fail_loc.get("LineNumber", "")
                short_fail = fail_file.split("redkeyoperator/")[-1]
                lines.append(f"- **Location:** [{short_fail}:{fail_line}]({fail_file}#L{fail_line})")
                lines.append("")

            lines.append("##### Message")
            lines.append("")
            lines.append("```")
            lines.append(failure.get("Message", "Unknown error"))
            lines.append("```")

            # Add stack trace if available
            stack_trace = fail_loc.get("FullStackTrace", "")
            if stack_trace:
                lines.append("")
                lines.append("##### Stack Trace")
                lines.append("")
                lines.append("```")
                lines.append(stack_trace)
                lines.append("```")

        # Add captured output if present
        if captured_output and captured_output.strip():
            lines.append("")
            lines.append("#### Captured Output")
            lines.append("")
            lines.append("```")
            lines.append(captured_output.strip())
            lines.append("```")

        lines.append("")

    return "\n".join(lines)


def unique(path: str) -> str:
    path = pathlib.Path(path)

    if not path.exists():
        return path

    stem = path.stem
    suffix = path.suffix
    parent = path.parent

    i = 1
    while True:
        candidate = parent / f"{stem}_{i}{suffix}"
        if not candidate.exists():
            return candidate
        i += 1




def main():
    if len(sys.argv) < 2:
        input_file = ".local/results.json"
    else:
        input_file = sys.argv[1]

    output_file = sys.argv[2] if len(sys.argv) > 2 else input_file.replace(".json", ".md")

    output_file = unique(output_file)

    print(f"Reading: {input_file}")
    with open(input_file, "r") as f:
        data = json.load(f)

    md_content = generate_md_report(data)

    print(f"Writing: {output_file}")
    with open(output_file, "w") as f:
        f.write(md_content)

    print("Done!")


if __name__ == "__main__":
    main()
