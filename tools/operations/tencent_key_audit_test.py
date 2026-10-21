import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("audit", Path(__file__).with_name("tencent_key_audit.py"))
audit = importlib.util.module_from_spec(spec)
spec.loader.exec_module(audit)


class InventoryTests(unittest.TestCase):
    def collector(self, responses):
        c = object.__new__(audit.Collector)
        c.call = lambda *args, **kwargs: responses.pop(0)
        return c

    def test_pagination_requires_complete_stable_total(self):
        c = self.collector([{"TotalNum": 3, "List": [1, 2]}, {"TotalNum": 3, "List": [3]}])
        self.assertEqual(c.paged("ListGroups", "List"), [1, 2, 3])
        for responses in (
            [{"TotalNum": 1}],
            [{"TotalNum": 1, "List": []}],
            [{"TotalNum": 2, "List": [1]}, {"TotalNum": 3, "List": [2]}],
        ):
            with self.assertRaises(ValueError):
                self.collector(responses).paged("ListGroups", "List")

    def test_masked_keys_and_completed_checkpoint_survive_partial_failure(self):
        kid = "AKIDabcdefghijklmnop"
        c = self.collector([{"AccessKeys": [{"AccessKeyId": kid, "Status": "Active"}]}])
        c.paged = lambda *args, **kwargs: []
        record = c.user({"Uin": 1, "Name": "service"})
        self.assertNotIn(kid, str(record))
        self.assertEqual(record["keys"][0]["id_masked"], "AKIDab****mnop")
        with tempfile.TemporaryDirectory() as directory:
            c.output = Path(directory)
            state = {"failures": {}}
            def fetch(x):
                if x == 2:
                    raise RuntimeError("read unavailable")
                return record
            with self.assertRaises(RuntimeError):
                c.collect_phase(state, "users", [1, 2], str, fetch)
            saved = audit.json.loads((c.output / "inventory.json").read_text())
            self.assertIn("1", saved["users"])
            self.assertIn("2", saved["failures"]["users"])
            c.collect_phase(saved, "users", [1, 2], str, lambda x: {"uin": x})
            self.assertEqual(saved["users"]["1"], record)
            self.assertEqual(saved["failures"]["users"], {})

    def test_membership_reconciliation_refreshes_both_views(self):
        c = object.__new__(audit.Collector)
        with tempfile.TemporaryDirectory() as directory:
            c.output = Path(directory)
            state = {"users": {"1": {"name": "service", "groups": []}},
                     "groups": {"2": {"GroupId": 2, "members": [{"Uin": 1}]}}}
            self.assertEqual(c.membership_mismatches(state), {"1": {"2"}})
            c.user = lambda u: {"name": u["Name"], "groups": [{"GroupId": 2}]}
            c.group = lambda g: g
            c.reconcile(state)
            self.assertEqual(c.membership_mismatches(state), {})


if __name__ == "__main__":
    unittest.main()
