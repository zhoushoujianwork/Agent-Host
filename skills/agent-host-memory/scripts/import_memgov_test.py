import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock


SPEC = importlib.util.spec_from_file_location("import_memgov", Path(__file__).with_name("import_memgov.py"))
IMPORT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(IMPORT)


class SourceImportTests(unittest.TestCase):
    def test_prepare_preserves_attribution_and_redacts_without_reading_tasks(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "workspace"
            (source / "memory").mkdir(parents=True)
            (source / "MEMORY.md").write_text("# Index\n")
            original = '# GitLab procedure for POP\nObserved 2026-09-24; pop was recorded, not rechecked.\npassword="fixture-sensitive-value"\nAuthorization: Bearer fixture-token-value\n'
            note = source / "memory" / "gitlab.md"
            note.write_text(original)
            (source / "memory" / "README.md").write_text("Navigation only")
            (source / "tasks").mkdir()
            (source / "tasks" / "private.md").write_text("private task must not be imported")
            manifest = IMPORT.prepare(source, root / "output", {"enabled": True, "audience_id": "owner", "scope": "fixture"})
            self.assertEqual(len(manifest["files"]), 2)
            self.assertEqual(len(manifest["batches"]), 1)
            envelope = json.loads((root / "output" / manifest["batches"][0]["path"]).read_text())
            item, evidence = envelope["items"][0], envelope["evidence"][0]
            self.assertEqual(item["kind"], "source_note")
            self.assertEqual(item["text"], evidence["tool"]["result"])
            self.assertEqual(evidence["role"], "tool")
            self.assertEqual([v["name"] for v in item["objects"]], ["GitLab", "POP"])
            self.assertIn("2026-09-24", item["text"])
            self.assertNotIn("fixture-sensitive-value", json.dumps(envelope))
            self.assertNotIn("fixture-token-value", json.dumps(envelope))
            self.assertNotIn("private task", json.dumps(envelope))
            self.assertEqual(note.read_text(), original)

    def test_chunks_preserve_unicode_and_byte_limits(self):
        original = "# 腾讯云\n" + "操作观察，不是当前状态。\n" * 1000
        chunks = list(IMPORT.chunks(original))
        self.assertEqual("".join(chunks), original)
        self.assertTrue(all(len(part.encode()) <= 6000 for part in chunks))

    def test_later_parts_retain_actual_subject_context(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "workspace"
            (source / "memory").mkdir(parents=True)
            (source / "MEMORY.md").write_text("# Index")
            original = "# Historical observations\n" + "Historical detail.\n" * 400 + "\nKonga was the recorded service.\n"
            (source / "memory" / "notes.md").write_text(original)
            output = Path(directory) / "output"
            manifest = IMPORT.prepare(source, output, {"enabled": True, "audience_id": "owner", "scope": "fixture"})
            envelope = json.loads((output / manifest["batches"][0]["path"]).read_text())
            self.assertGreater(len(envelope["items"]), 1)
            for item, evidence in zip(envelope["items"], envelope["evidence"]):
                self.assertEqual(evidence["tool"]["result"], item["text"])
                contexts = evidence["tool"]["input"]["source_subject_context"]
                self.assertTrue(all(context in original for context in contexts))
                self.assertIn("Konga", json.dumps(evidence["tool"], ensure_ascii=False))

    def test_source_symlinks_fail_explicitly(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "workspace"
            (source / "memory").mkdir(parents=True)
            (source / "MEMORY.md").write_text("# Index")
            (source / "memory" / "linked.md").symlink_to(source / "MEMORY.md")
            with self.assertRaisesRegex(ValueError, "symlinks"):
                IMPORT.prepare(source, root / "output", {"enabled": True, "audience_id": "owner", "scope": "fixture"})
            with self.assertRaisesRegex(ValueError, "read-only source Workspace"):
                IMPORT.prepare(source, source / "output", {"enabled": True, "audience_id": "owner", "scope": "fixture"})

    def test_unknown_transport_checks_receipt_before_identical_resend(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "workspace"
            (source / "memory").mkdir(parents=True)
            (source / "MEMORY.md").write_text("# Index")
            (source / "memory" / "note.md").write_text("# Konga historical note\nSource reference only.")
            output = root / "output"
            binding = {"enabled": True, "audience_id": "owner", "scope": "fixture"}
            manifest = IMPORT.prepare(source, output, binding)
            batch = manifest["batches"][0]
            batch.update(attempted=True, status="submitting")
            envelope = output / batch["path"]
            original = envelope.read_bytes()
            calls = []

            def cli(_binary, _connection, *args):
                calls.append(args)
                if args == ("connection", "show"):
                    return {"binding": binding}
                if args[0] == "receipt":
                    self.assertEqual(args, ("receipt", "--key", batch["key"]))
                    raise RuntimeError("agent-host: sql: no rows in result set")
                self.assertEqual(args, ("submit", "--input", str(envelope)))
                self.assertEqual(envelope.read_bytes(), original)
                return {"id": "receipt", "processing_status": "completed", "results": [{"status": "excluded", "reason": "fixture"}]}

            with mock.patch.object(IMPORT, "cli", side_effect=cli):
                IMPORT.submit("fixture", "default", output, manifest)
            self.assertEqual([call[0] for call in calls], ["connection", "receipt", "submit"])
            self.assertEqual(batch["status"], "completed")


if __name__ == "__main__":
    unittest.main()
