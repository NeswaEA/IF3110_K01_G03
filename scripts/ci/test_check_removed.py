from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("check-removed.sh")
PATTERNS = ["html/template", ".gohtml", "data-widget", "mountWidget", "internal/webui", "migration_drafts", "migration_runs",
            "migration-drafts", "migration-runs", "migration_ledger", "MIGRATION_TIMEOUT", "Idempotency-Key", "RequirePage",
            "RedirectToLogin"]


class RemovedArchitecture(unittest.TestCase):
    def run_check(self, files):
        with tempfile.TemporaryDirectory() as root:
            for name, body in {"backend/main.go": "package main\n", **files}.items():
                path = Path(root) / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(body)
            return subprocess.run(["sh", str(SCRIPT), root], capture_output=True, text=True)

    def test_clean_tree_passes(self):
        result = self.run_check({"web/src/main.tsx": "export {};\n"})
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_every_planted_pattern_fails(self):
        for pattern in PATTERNS:
            with self.subTest(pattern=pattern):
                result = self.run_check({"scripts/dev.sh": f"echo {pattern}\n"})
                self.assertEqual(result.returncode, 1)
                self.assertIn(pattern, result.stderr)

    def test_removed_file_names_fail(self):
        result = self.run_check({"backend/internal/webui/templates/layout.gohtml": "<html></html>\n"})
        self.assertEqual(result.returncode, 1)
        self.assertIn("layout.gohtml", result.stderr)

    def test_only_source_directories_are_checked_case_sensitively(self):
        result = self.run_check({"docs/history.md": "migration_drafts\n", "infra/notes.txt": "Migration_Drafts\n",
                                 "web/src/node_modules/x/index.js": "mountWidget\n", "scripts/ci/check-removed.sh": "RequirePage\n"})
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_allowed_files_are_exempt_only_for_themselves(self):
        allowed = {"backend/migrations/0006_migration_workflow.sql": "DROP TABLE migration_runs, migration_drafts;\n",
                   "backend/internal/migrations/history_init.go": "_platform.migration_ledger\n"}
        self.assertEqual(self.run_check(allowed).returncode, 0)
        result = self.run_check({**allowed, "backend/migrations/0009_more.sql": "DROP TABLE migration_runs;\n"})
        self.assertEqual(result.returncode, 1)
        self.assertIn("0009_more.sql", result.stderr)
        self.assertNotIn("0006", result.stderr)

    def test_binary_files_are_ignored(self):
        self.assertEqual(self.run_check({"scripts/cache.pyc": "\0RequirePage"}).returncode, 0)


if __name__ == "__main__":
    unittest.main()
