import json
import tempfile
import unittest
from pathlib import Path

from check_address_pool import configured


class AddressPoolTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.path = Path(self.directory.name) / "daemon.json"

    def test_missing_or_unreadable_configuration_is_refused(self):
        self.assertFalse(configured(self.path))
        self.path.mkdir()
        self.assertFalse(configured(self.path))

    def test_existing_configuration_without_both_network_settings_is_refused(self):
        for config in (
            {},
            {"log-driver": "json-file"},
            {"bip": "10.96.127.1/24"},
            {"default-address-pools": [{"base": "10.96.64.0/19", "size": 24}]},
            {"bip": "10.96.127.1/24", "default-address-pools": []},
        ):
            with self.subTest(config=config):
                self.path.write_text(json.dumps(config), encoding="utf-8")
                self.assertFalse(configured(self.path))

    def test_malformed_configuration_is_refused(self):
        for text in ("", "{", "null", "[]", '"text"', "\xff"):
            with self.subTest(text=text):
                self.path.write_text(text, encoding="utf-8")
                self.assertFalse(configured(self.path))
        self.path.write_bytes(b"\xff")
        self.assertFalse(configured(self.path))

    def test_invalid_addresses_and_pool_sizes_are_refused(self):
        valid = {"bip": "10.96.127.1/24", "default-address-pools": [{"base": "10.96.64.0/19", "size": 24}]}
        for key, value in (
            ("bip", "not-an-address"),
            ("bip", "fd00::1/64"),
            ("bip", None),
            ("bip", True),
            ("bip", "10.96.127.1"),
            ("default-address-pools", {}),
            ("default-address-pools", [None]),
            ("default-address-pools", [{"base": "not-a-network", "size": 24}]),
            ("default-address-pools", [{"base": "10.96.64.0/19", "size": 18}]),
            ("default-address-pools", [{"base": "10.96.64.0/19", "size": 33}]),
            ("default-address-pools", [{"base": "10.96.64.0/19", "size": "24"}]),
            ("default-address-pools", [{"base": "0.0.0.0/0", "size": True}]),
            ("default-address-pools", [{"base": "fd00::/48", "size": 64}]),
        ):
            with self.subTest(key=key, value=value):
                self.path.write_text(json.dumps(valid | {key: value}), encoding="utf-8")
                self.assertFalse(configured(self.path))

    def test_runbook_configuration_and_unrelated_settings_are_accepted(self):
        self.path.write_text(
            json.dumps(
                {
                    "bip": "10.96.127.1/24",
                    "default-address-pools": [{"base": "10.96.64.0/19", "size": 24}],
                    "log-driver": "json-file",
                }
            ),
            encoding="utf-8",
        )
        self.assertTrue(configured(self.path))


if __name__ == "__main__":
    unittest.main()
