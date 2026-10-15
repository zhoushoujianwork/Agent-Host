import json
from pathlib import Path
import sqlite3
import tempfile
import unittest

from observe import observe


class ObservationTests(unittest.TestCase):
    def test_correlates_exact_skill_revision_without_exporting_source_text(self):
        with tempfile.TemporaryDirectory() as home:
            with sqlite3.connect(Path(home) / "state.db") as db:
                db.execute("CREATE TABLE runs(id TEXT,created_at TEXT,payload TEXT)")
                db.execute("CREATE TABLE events(run_id TEXT,seq INTEGER,payload TEXT)")
                run = {"id": "run", "input": "PRIVATE source", "created_at": "2026-10-15T04:00:00Z"}
                db.execute("INSERT INTO runs VALUES(?,?,?)", ("run", run["created_at"], json.dumps(run)))
                events = [
                    {"type": "workspace.assembled", "data": {"skill_manifest": {"sources": [{"name": "procedure-one", "memory_id": "memory", "revision": 2}]}}},
                    {"type": "experience.learning.summary", "data": {"drafts": 2, "eligible": 1, "refinement_attempted": True, "refined_eligible": 1}},
                    {"phase": "execute", "data": {"tool": {"id": "skill", "stage": "call", "name": "Skill", "input": {"skill": "agent-host:procedure-one"}}}},
                    {"phase": "execute", "data": {"tool": {"id": "skill", "stage": "result", "result": "PRIVATE credential", "is_error": False}}},
                    {"phase": "execute", "data": {"tool": {"id": "unpaired", "stage": "result", "result": "PRIVATE credential", "is_error": False}}},
                    {"phase": "execute", "data": {"tool": {"id": "bad", "stage": "call", "name": "Bash", "input": {"command": "tccli cbs DescribeDisks --cli-profile fixture"}}}},
                    {"phase": "execute", "data": {"tool": {"id": "bad", "stage": "result", "result": "Unknown options: --cli-profile", "is_error": True}}},
                ]
                for seq, event in enumerate(events):
                    db.execute("INSERT INTO events VALUES(?,?,?)", ("run", seq, json.dumps(event)))
            report = observe(home, "2026-10-15T03:00:00Z")
            item = report["runs"][0]
            self.assertEqual(item["skill_uses"], [{"memory_id": "memory", "revision": 2, "tool_id": "skill"}])
            self.assertEqual(item["tccli_unknown_profile_flag"], 1)
            self.assertEqual(item["learning"]["refined_eligible"], 1)
            self.assertNotIn("PRIVATE", json.dumps(report))
            self.assertEqual(len(observe(home, "2026-10-15T04:00:00Z")["runs"]), 1)
            self.assertEqual(observe(home, "2026-10-16T00:00:00Z")["runs"], [])

    def test_rejects_unbounded_sample(self):
        with self.assertRaises(ValueError):
            observe("/unused", limit=101)


if __name__ == "__main__":
    unittest.main()
