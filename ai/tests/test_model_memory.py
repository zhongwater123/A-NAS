"""Architecture-specific admission budgets must preserve the default pins."""

import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from anas_ai import providers


class ModelMemoryTest(unittest.TestCase):
    def read(self, manifest, machine):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "manifest.json"
            path.write_text(json.dumps(manifest), encoding="utf-8")
            with patch("platform.machine", return_value=machine):
                return providers.read_manifest(path)

    def test_arm_uses_its_measured_budget(self):
        pin = {"memoryBytes": 64, "memoryBytesByArchitecture": {"arm64": 128}}
        for machine in ("aarch64", "arm64"):
            with self.subTest(machine=machine):
                self.assertEqual(self.read(pin, machine)["memoryBytes"], 128)

    def test_other_machines_keep_the_default_budget(self):
        pin = {"memoryBytes": 64, "memoryBytesByArchitecture": {"arm64": 128}}
        for machine in ("x86_64", "AMD64", "unknown"):
            with self.subTest(machine=machine):
                self.assertEqual(self.read(pin, machine)["memoryBytes"], 64)

    def test_manifests_without_overrides_keep_their_budget(self):
        self.assertEqual(self.read({"memoryBytes": 64}, "aarch64")["memoryBytes"], 64)

    def test_invalid_selected_budget_fails_closed(self):
        for value in (-1, True, "128"):
            with self.subTest(value=value), self.assertRaises(providers.Unavailable):
                self.read({"memoryBytes": 64, "memoryBytesByArchitecture": {"arm64": value}}, "aarch64")

    def test_invalid_override_map_fails_closed(self):
        with self.assertRaises(providers.Unavailable):
            self.read({"memoryBytes": 64, "memoryBytesByArchitecture": []}, "aarch64")
