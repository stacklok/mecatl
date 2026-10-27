#!/usr/bin/env python3
"""Offline contracts for the two Playwright job containers and coordinated updates."""

import json
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[2]
JOBS = {"sdk-browser": "sdk/typescript", "studio-browser": "apps"}


def validate(workflow, packages, renovate):
    images = []
    for job, package in JOBS.items():
        block = re.search(rf"^  {job}:\n(.*?)(?=^  [\w-]+:|\Z)", workflow, re.M | re.S)
        assert block, f"missing {job}"
        block = block[1]
        image = re.search(r"^      image: (\S+)$", block, re.M)
        assert image, f"{job}: missing container image"
        version = re.fullmatch(
            r"mcr\.microsoft\.com/playwright:v(\d+\.\d+\.\d+)-noble@sha256:[0-9a-f]{64}",
            image[1],
        )
        assert version, f"{job}: image must have an exact version and digest"
        assert version[1] == packages[package]["devDependencies"]["@playwright/test"], (
            f"{job}: image and @playwright/test versions differ"
        )
        images.append(image[1])
        assert "options: --user 1001:1001 --init --ipc=host" in block, job
        assert "shell: bash" in block, job
        assert "timeout-minutes: 20" in block, job
        assert "playwright install" not in block and "browser:install" not in block, job
        assert "playwright-cache" not in block and "~/.cache/ms-playwright" not in block, job
        assert "if: failure()" in block and "include-hidden-files: true" in block, job
        assert f"name: {job}-${{{{ matrix.project }}}}" in block, job
        assert f"path: .scratch/{'sdk' if job == 'sdk-browser' else 'studio'}-browser-playwright/" in block, job
    assert images[0] == images[1], "browser jobs must use the same image"
    rule = next(r for r in renovate["packageRules"] if r.get("groupName") == "playwright")
    assert set(rule["matchPackageNames"]) == {
        "@playwright/test", "playwright", "playwright-core", "mcr.microsoft.com/playwright"
    }, "Playwright npm packages and Docker image must update together"


class PlaywrightCI(unittest.TestCase):
    def setUp(self):
        self.workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        self.packages = {p: json.loads((ROOT / p / "package.json").read_text()) for p in JOBS.values()}
        self.renovate = json.loads((ROOT / "renovate.json").read_text())

    def check(self, workflow=None):
        validate(self.workflow if workflow is None else workflow, self.packages, self.renovate)

    def test_repository(self):
        self.check()

    def test_rejects_version_skew(self):
        for package in self.packages.values():
            with self.subTest(package=package["name"]):
                version = package["devDependencies"]["@playwright/test"]
                package["devDependencies"]["@playwright/test"] = "0.0.0"
                with self.assertRaisesRegex(AssertionError, "versions differ"):
                    self.check()
                package["devDependencies"]["@playwright/test"] = version

    def test_rejects_missing_pins_and_runtime_installs(self):
        for before, after in [
            (r"@sha256:[0-9a-f]{64}", ""),
            (r"(  sdk-browser:\n)", r"\1    # playwright install --with-deps\n"),
            (r"(  studio-browser:\n)", r"\1    # task studio:browser:install\n"),
            (r"include-hidden-files: true", "include-hidden-files: false"),
            (r"--user 1001:1001 ", ""),
        ]:
            with self.subTest(before=before), self.assertRaises(AssertionError):
                self.check(re.sub(before, after, self.workflow))

    def test_rejects_split_update_groups(self):
        rule = next(r for r in self.renovate["packageRules"] if r.get("groupName") == "playwright")
        rule["matchPackageNames"].remove("mcr.microsoft.com/playwright")
        with self.assertRaisesRegex(AssertionError, "update together"):
            self.check()


if __name__ == "__main__":
    unittest.main()
