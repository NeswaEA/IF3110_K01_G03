import argparse
import json
import os
from pathlib import Path
import sys


def check(path):
    counts = {"pass": 0, "fail": 0, "skip": 0}
    failures = []
    for line in path.read_text().splitlines():
        if not line.strip():
            continue
        event = json.loads(line)
        action = event.get("Action")
        test = event.get("Test")
        if test and action in counts:
            counts[action] += 1
        if action == "fail" or (test and action == "skip"):
            failures.append(event.get("Package", "") + ("/" + test if test else ""))

    report = f"Go tests: {counts['pass']} passed, {counts['fail']} failed, {counts['skip']} skipped."
    for name in failures:
        report += "\n- " + name
    if not counts["pass"]:
        report += "\nNo test cases passed; check the package list and test filter."
    return report, bool(counts["pass"]) and not failures


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("log", type=Path)
    args = parser.parse_args()
    try:
        report, passed = check(args.log)
    except (OSError, ValueError, AttributeError) as err:
        report, passed = f"Cannot read Go test results: {err}", False
    print(report)
    if summary := os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(summary, "a") as output:
            output.write(report + "\n")
    return 0 if passed else 1


if __name__ == "__main__":
    sys.exit(main())
