#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Execute the omnibus CI gate against offline job-result fixtures."""

import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = (ROOT / ".github/workflows/ci.yml").read_text()
def job_blocks(workflow):
    return dict(re.findall(
        r"^  ([\w-]+):\n(.*?)(?=^  [\w-]+:|\Z)",
        workflow.split("jobs:\n", 1)[1], re.M | re.S,
    ))


BLOCKS = job_blocks(WORKFLOW)
GATE = BLOCKS["ci-gate"]
SCRIPT = textwrap.dedent(GATE.split("        run: |\n", 1)[1])
ALWAYS = "changes test lint studio-gate user-docs-gate docs domain-model".split()
GO = "build analysis module-lint action-scripts fuzz-smoke api-compat vuln".split()
SDK_RUNTIME = "sdk-integration sdk-deno sdk-browser slack-bot-example".split()
STUDIO = "studio studio-browser".split()
MACOS_GO = "smoke-darwin-arm64 managed-temp-macos".split()
FLAGS = "docs_only go_relevant sdk_relevant studio_relevant site_relevant macos_go macos_sdk".split()


def fixture(required=(), true_flags=()):
    needs = {
        job: {"result": "success" if job in ALWAYS or job in required else "skipped"}
        for job in BLOCKS if job != "ci-gate"
    }
    needs["changes"]["outputs"] = {
        flag: "true" if flag in true_flags else "false" for flag in FLAGS
    }
    return needs


def execute(needs, event="pull_request", draft="false"):
    # The real gate has no checkout and must need only the supplied metadata.
    with tempfile.TemporaryDirectory() as cwd:
        return subprocess.run(
            [sys.executable, "-c", SCRIPT], cwd=cwd, capture_output=True, text=True,
            env={"NEEDS_JSON": json.dumps(needs), "EVENT_NAME": event, "PR_DRAFT": draft},
        )


class CIGate(unittest.TestCase):
    def check_wiring(self, workflow):
        blocks = job_blocks(workflow)
        gate = blocks["ci-gate"]
        dependencies = re.findall(r"^      - ([\w-]+)$", gate.split("    if:", 1)[0], re.M)
        self.assertCountEqual(dependencies, set(blocks) - {"ci-gate"})
        self.assertIn("    name: CI gate\n", gate)
        self.assertIn("    if: always()\n", gate)
        self.assertNotIn("continue-on-error:", gate)
        for block in blocks.values():
            self.assertNotRegex(block, re.compile(r"^    continue-on-error:", re.M))
        self.assertIn("        shell: python\n", gate)
        self.assertIn("          NEEDS_JSON: ${{ toJSON(needs) }}\n", gate)
        self.assertIn("          EVENT_NAME: ${{ github.event_name }}\n", gate)
        self.assertIn("          PR_DRAFT: ${{ github.event.pull_request.draft }}\n", gate)
        self.assertIn("python3 .github/scripts/ci-gate_test.py", blocks["changes"])
        self.assertIn("python3 .github/scripts/ci-gate_test.py", (ROOT / "Taskfile.yml").read_text())

    def test_workflow_wiring(self):
        self.check_wiring(WORKFLOW)

    def test_wiring_guard_rejects_uncovered_jobs_and_bypasses(self):
        for workflow in [
            WORKFLOW + "\n  new-job:\n    runs-on: ubuntu-24.04\n    steps:\n      - run: echo new\n",
            WORKFLOW.replace("      - build\n", "", 1),
            WORKFLOW.replace("  ci-gate:\n", "  ci-gate:\n    continue-on-error: true\n"),
            WORKFLOW.replace(GATE, GATE.replace("    if: always()\n", "    if: success()\n")),
        ]:
            with self.subTest(workflow=workflow[-100:]), self.assertRaises(AssertionError):
                self.check_wiring(workflow)

    def test_expected_conditions_match_workflow(self):
        non_docs = "needs.changes.outputs.docs_only != 'true'"
        ready = "(github.event_name != 'pull_request' || github.event.pull_request.draft == false)"
        groups = [
            (None, ["changes", "docs", "domain-model"]),
            ("${{ !cancelled() }}", ["test"]),
            ("always()", ["lint", "studio-gate", "user-docs-gate", "ci-gate"]),
            ("needs.changes.outputs.sdk_relevant == 'true'", ["sdk"]),
            ("needs.changes.outputs.site_relevant == 'true'", ["user-docs"]),
            (non_docs + " && needs.changes.outputs.go_relevant == 'true'", GO),
            (non_docs + " && (needs.changes.outputs.go_relevant == 'true' || needs.changes.outputs.sdk_relevant == 'true')", SDK_RUNTIME),
            (non_docs + " && (needs.changes.outputs.studio_relevant == 'true' || needs.changes.outputs.go_relevant == 'true')", STUDIO),
            (non_docs + " && (needs.changes.outputs.go_relevant == 'true' || needs.changes.outputs.sdk_relevant == 'true' || needs.changes.outputs.studio_relevant == 'true')", ["mecated"]),
            (non_docs + " && needs.changes.outputs.macos_go == 'true' && " + ready, MACOS_GO),
            (non_docs + " && needs.changes.outputs.macos_sdk == 'true' && " + ready, ["sdk-macos-spawn"]),
        ]
        for expected, jobs in groups:
            for job in jobs:
                with self.subTest(job=job):
                    match = re.search(r"^    if: ([^\n]+)((?:\n      [^\n]+)*)", BLOCKS[job], re.M)
                    actual = None if match is None else match[1]
                    if actual == ">-":
                        actual = " ".join(match[2].split())
                    self.assertEqual(actual, expected)

    def test_applicable_coverage(self):
        cases = [
            ("docs", [], ["docs_only"]),
            ("user-docs", ["user-docs"], ["docs_only", "site_relevant"]),
            ("site", ["user-docs"], ["site_relevant"]),
            ("studio", ["mecated"] + STUDIO, ["studio_relevant"]),
            ("sdk-without-studio", ["sdk", "mecated"] + SDK_RUNTIME, ["sdk_relevant"]),
            ("go-without-macos", ["mecated"] + GO + SDK_RUNTIME + STUDIO, ["go_relevant"]),
            ("independent-macos", MACOS_GO + ["sdk-macos-spawn"], ["macos_go", "macos_sdk"]),
            ("docs-with-sdk", ["sdk"], ["docs_only", "sdk_relevant", "macos_go", "macos_sdk"]),

            ("sdk", ["sdk", "mecated", "sdk-macos-spawn"] + SDK_RUNTIME + STUDIO,
             ["sdk_relevant", "studio_relevant", "macos_sdk"]),
            ("go", ["mecated", "sdk-macos-spawn"] + GO + SDK_RUNTIME + STUDIO + MACOS_GO,
             ["go_relevant", "macos_go", "macos_sdk"]),
            ("full", set(BLOCKS) - {"ci-gate"}, set(FLAGS) - {"docs_only"}),
        ]
        for name, required, flags in cases:
            for event, draft in [("pull_request", "false"), ("pull_request", "true"),
                                 ("push", ""), ("workflow_dispatch", "")]:
                with self.subTest(case=name, event=event, draft=draft):
                    applicable = set(required)
                    if event == "pull_request" and draft == "true":
                        applicable -= set(MACOS_GO + ["sdk-macos-spawn"])
                    result = execute(fixture(applicable, flags), event, draft)
                    self.assertEqual(result.returncode, 0, result.stderr)

    def test_every_dependency_must_succeed_when_applicable(self):
        needs = fixture(set(BLOCKS) - {"ci-gate"}, set(FLAGS) - {"docs_only"})
        for job in needs:
            for status in ("failure", "cancelled", "skipped", ""):
                with self.subTest(job=job, status=status):
                    broken = json.loads(json.dumps(needs))
                    broken[job]["result"] = status
                    self.assertNotEqual(execute(broken).returncode, 0)

    def test_inapplicable_jobs_must_be_skipped(self):
        needs = fixture(true_flags=["docs_only"])
        for job in set(needs) - set(ALWAYS):
            for status in ("success", "failure", "cancelled"):
                with self.subTest(job=job, status=status):
                    broken = json.loads(json.dumps(needs))
                    broken[job]["result"] = status
                    self.assertNotEqual(execute(broken).returncode, 0)

    def test_missing_or_invalid_classification_is_rejected(self):
        for flag in FLAGS:
            for value in (None, "", "invalid"):
                with self.subTest(flag=flag, value=value):
                    needs = fixture(true_flags=["docs_only"])
                    if value is None:
                        del needs["changes"]["outputs"][flag]
                    else:
                        needs["changes"]["outputs"][flag] = value
                    self.assertNotEqual(execute(needs).returncode, 0)

    def test_missing_or_invalid_pr_draft_is_rejected(self):
        for draft in ("", "invalid"):
            with self.subTest(draft=draft):
                self.assertNotEqual(execute(fixture(true_flags=["docs_only"]), draft=draft).returncode, 0)

    def test_dependency_set_must_match(self):
        needs = fixture(true_flags=["docs_only"])
        del needs["build"]
        self.assertNotEqual(execute(needs).returncode, 0)
        needs = fixture(true_flags=["docs_only"])
        needs["unhandled-job"] = {"result": "skipped"}
        self.assertNotEqual(execute(needs).returncode, 0)


if __name__ == "__main__":
    unittest.main()
