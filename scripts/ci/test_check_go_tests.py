import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


spec = importlib.util.spec_from_file_location("check_go_tests", Path(__file__).with_name("check-go-tests.py"))
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)


class GoTestResults(unittest.TestCase):
    def check_events(self, events):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "tests.jsonl"
            path.write_text("\n".join(json.dumps(event) for event in events))
            return checker.check(path)

    def test_passed_cases_and_packages_without_tests(self):
        report, passed = self.check_events([
            {"Action": "skip", "Package": "example/no-tests"},
            {"Action": "pass", "Package": "example/tested", "Test": "TestExample"},
            {"Action": "pass", "Package": "example/tested"},
        ])
        self.assertTrue(passed)
        self.assertIn("1 passed, 0 failed, 0 skipped", report)

    def test_skipped_cases_fail_even_when_other_cases_pass(self):
        report, passed = self.check_events([
            {"Action": "pass", "Package": "example", "Test": "TestUnit"},
            {"Action": "skip", "Package": "example", "Test": "TestIntegration"},
        ])
        self.assertFalse(passed)
        self.assertIn("example/TestIntegration", report)
        self.assertIn("1 skipped", report)

    def test_test_and_package_failures_fail(self):
        for event in [
            {"Action": "fail", "Package": "example", "Test": "TestFailed"},
            {"Action": "fail", "Package": "example"},
        ]:
            with self.subTest(event=event):
                report, passed = self.check_events([
                    {"Action": "pass", "Package": "example", "Test": "TestUnit"}, event,
                ])
                self.assertFalse(passed)
                self.assertIn("example", report)

    def test_empty_logs_and_filters_matching_no_tests_fail(self):
        for events in [[], [{"Action": "pass", "Package": "example"}]]:
            with self.subTest(events=events):
                report, passed = self.check_events(events)
                self.assertFalse(passed)
                self.assertIn("No test cases passed", report)

    def test_malformed_output_fails(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "tests.jsonl"
            path.write_text("invalid output")
            with self.assertRaises(ValueError):
                checker.check(path)


if __name__ == "__main__":
    unittest.main()
