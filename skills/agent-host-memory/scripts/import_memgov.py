#!/usr/bin/env python3
"""Import selected saved Markdown as attributed source notes through Host CLI."""

import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import time


SUBJECTS = [
    ("BuildHub|Build Hub", "service"), ("GitLab|gitlab", "system"),
    ("Flink|flink", "system"), ("N9E|n9e", "system"),
    ("tccli", "tool"), ("Konga|konga", "system"),
    ("ModelHub", "service"), ("Codex", "tool"),
    ("GTM SkillLab|SkillLab", "project"), ("Multiboard|MultiBoard|multiboard", "project"),
    ("CBS|cbs", "tool"), ("POP|pop", "system"), ("Redis|redis", "system"),
    ("CFD|cfd", "system"), ("SkillHub|Skillhub", "service"),
    ("GooseFS|goosefs", "system"), ("Kubernetes|kubernetes", "system"),
    ("Helm|helm", "tool"), ("OPS", "system"), ("PatSnap|patsnap", "organization"),
    ("CC", "tool"), ("周守健", "person"),
]
SECRET_ASSIGNMENT = re.compile(
    r'''(?i)((?:["']?)(?:[A-Za-z_]*password|passwd|secret_?key|secret_?id|api_?key|access_?token|auth_?token|authorization)["']?\s*[:=]\s*)("[^"\n]*"|'[^'\n]*'|[^\s,;}]+)''')
SECRET_OPTIONS = re.compile(r"(?i)(--(?:password|passwd|secret-key|secret-id|api-key|token)\s+)(\S+)")
TOKEN = re.compile(r"\b(?:AKIA[0-9A-Z]{16}|AKID[A-Za-z0-9]{20,}|sk-[A-Za-z0-9_-]{20,}|glpat-[A-Za-z0-9_-]{15,})\b")
PRIVATE_KEY = re.compile(r"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----.*?-----END (?:RSA |EC |OPENSSH )?PRIVATE KEY-----", re.S)
URL_CREDENTIAL = re.compile(r"(https?://)[^\s/@]+:[^\s/@]+@")
AUTH_HEADER = re.compile(r"(?i)(Authorization[\"']?\s*:\s*[\"']?(?:Bearer|Basic)\s+)[A-Za-z0-9._~+/=-]+")


def digest(data):
    return hashlib.sha256(data).hexdigest()


def write_json(path, value):
    raw = (json.dumps(value, ensure_ascii=False, indent=2) + "\n").encode()
    temporary = path.with_suffix(path.suffix + ".new")
    with temporary.open("wb") as stream:
        os.chmod(temporary, 0o600)
        stream.write(raw)
    temporary.replace(path)


def redact(text):
    count = 0

    def assignment(match):
        nonlocal count
        value = match[2].strip("\"'")
        if value.startswith(("<", "$", "[REDACTED", "***", "os.environ")) or value in ("null", "None", "true", "false"):
            return match[0]
        count += 1
        return match[1] + '"[REDACTED]"'

    text, removed_headers = AUTH_HEADER.subn(r"\1[REDACTED]", text)
    count += removed_headers
    text = SECRET_ASSIGNMENT.sub(assignment, text)
    text = SECRET_OPTIONS.sub(assignment, text)
    for pattern, replacement in ((TOKEN, "[REDACTED]"), (PRIVATE_KEY, "[REDACTED PRIVATE KEY]"), (URL_CREDENTIAL, r"\1[REDACTED]@")):
        text, replaced = pattern.subn(replacement, text)
        count += replaced
    return text, count


def chunks(text, maximum=6000):
    current = ""
    for line in text.splitlines(keepends=True):
        for char in line:
            if len((current + char).encode()) > maximum:
                yield current
                current = ""
            current += char
    if current.strip():
        yield current


def subject_context(text, subjects):
    # A later part may omit the document's subject. Preserve actual nearby source
    # text as read metadata rather than inventing a name in the quoted part.
    excerpts = []
    for subject in subjects:
        index = text.find(subject["name"])
        if index >= 0:
            excerpts.append(text[max(0, index - 80):index + len(subject["name"]) + 80])
    return excerpts


def prepare(workspace, output, binding):
    if not workspace.is_dir() or not (workspace / "MEMORY.md").is_file() or not (workspace / "memory").is_dir():
        raise ValueError("provide the active memgov Workspace containing MEMORY.md and memory/")
    if (workspace / "memory").is_symlink() or output.resolve().is_relative_to(workspace.resolve()):
        raise ValueError("keep the memory source local and output outside the read-only source Workspace")
    if binding.get("audience_id") != "owner" or not binding.get("enabled") or not binding.get("scope"):
        raise ValueError("destination must be an enabled explicit owner-private binding")
    output.mkdir(parents=True, exist_ok=True, mode=0o700)
    (output / "sources").mkdir(exist_ok=True, mode=0o700)
    (output / "envelopes").mkdir(exist_ok=True, mode=0o700)
    manifest = {"source_workspace": str(workspace), "binding": binding, "files": [], "batches": [], "scope": "saved memory Markdown only; no tasks, artifacts, raw chats, backups or source commands"}
    observed = datetime.datetime.now(datetime.timezone.utc).isoformat()
    entries = []
    for directory, dirs, files in os.walk(workspace / "memory", followlinks=False):
        dirs.sort()
        for name in dirs + files:
            if (Path(directory) / name).is_symlink():
                raise ValueError("symlinks within the memory source are not imported")
        for name in sorted(files):
            path = Path(directory) / name
            if path.suffix.lower() != ".md":
                continue
            relative = path.relative_to(workspace)
            raw = path.read_bytes()
            text, redactions = redact(raw.decode("utf-8"))
            saved = output / "sources" / relative
            saved.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            saved.write_text(text)
            saved.chmod(0o600)
            row = {"path": str(relative), "source_sha256": digest(raw), "snapshot_sha256": digest(text.encode()), "redactions": redactions}
            manifest["files"].append(row)
            if name in ("README.md", "MIGRATED.md", "workspace-acceptance.md") or name.startswith("legacy-index-"):
                row.update(status="excluded", reason="navigation_or_acceptance_record")
                continue
            heading = re.search(r"(?m)^#\s+(.+)$", text)
            title = heading[1] if heading else path.stem
            subjects = []
            # Prefer subjects in the source heading, then literal source content.
            for source in (title, text):
                for pattern, kind in SUBJECTS:
                    match = re.search(pattern, source)
                    if match and all(v["name"].casefold() != match[0].casefold() for v in subjects):
                        subjects.append({"name": match[0], "kind": kind, "evidence_id": "read"})
                    if len(subjects) == 3:
                        break
                if len(subjects) == 3:
                    break
            if not subjects:
                row.update(status="excluded", reason="no_source_grounded_subject")
                continue
            parts = list(chunks(text))
            row.update(status="prepared", parts=len(parts))
            for index, part in enumerate(parts, 1):
                label = title if len(parts) == 1 else f"{title} ({index}/{len(parts)})"
                while len(label.encode()) > 256:
                    label = label[:-1]
                evidence = {"id": "read", "role": "tool", "author": "memgov Markdown importer", "observed_at": observed,
                            "text": "Saved Markdown read during import; selected derived excerpt, not an execution of the documented commands. Credential-like values redacted where detected.",
                            "tool": {"name": "ReadRedactedMarkdown", "input": {"path": str(path), "snapshot": str(saved), "document_title": title, "source_sha256": digest(raw), "redactions": redactions, "part": index, "parts": len(parts), "source_date_context": text[:700], "source_subject_context": subject_context(text, subjects)}, "result": part, "is_error": False}}
                item = {"kind": "source_note", "title": label, "text": part, "evidence_ids": ["read"], "objects": subjects}
                entries.append((item, evidence, row["source_sha256"], str(relative)))
    # Each envelope is independently resumable. Persist its original observation
    # time and body; retries read the saved envelope rather than regenerating it.
    for number, group in enumerate([entries[i:i + 3] for i in range(0, len(entries), 3)], 1):
        items, evidence = [], []
        for index, (item, source, _, _) in enumerate(group):
            reference = f"read-{index}"
            item = json.loads(json.dumps(item))
            item["evidence_ids"] = [reference]
            for obj in item["objects"]:
                obj["evidence_id"] = reference
            items.append(item)
            evidence.append({**source, "id": reference})
        key = "memgov-note-" + digest(json.dumps({"items": items, "evidence": evidence}, sort_keys=True, ensure_ascii=False).encode())[:40]
        envelope = {"schema_version": 1, "idempotency_key": key, "source": {"entry": "memgov-markdown-import", "observed_at": observed}, "items": items, "evidence": evidence}
        path = output / "envelopes" / f"{number:03d}.json"
        if len(json.dumps(envelope, ensure_ascii=False).encode()) > 64 * 1024:
            raise ValueError("prepared envelope exceeds Host limit")
        write_json(path, envelope)
        manifest["batches"].append({"path": str(path.relative_to(output)), "key": key, "sources": [v[3] for v in group], "status": "prepared"})
    write_json(output / "manifest.json", manifest)
    return manifest


def cli(binary, connection, *args):
    result = subprocess.run([binary, "memory", *args, "--connection", connection], capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError(result.stderr.strip() or "Host CLI failed")
    return json.loads(result.stdout)


def submit(binary, connection, output, manifest):
    current = cli(binary, connection, "connection", "show")["binding"]
    if current != manifest["binding"]:
        raise ValueError("destination binding changed; review before importing")
    for index, batch in enumerate(manifest["batches"], 1):
        if batch["status"] in ("completed", "failed"):
            continue
        if batch.get("receipt_id") or batch.get("attempted"):
            try:
                receipt = cli(binary, connection, "receipt", "--key", batch["key"])
            except RuntimeError as error:
                if str(error) != "agent-host: sql: no rows in result set":
                    raise
                print(json.dumps({"key": batch["key"], "receipt_lookup": "absent", "action": "resubmit_identical_saved_envelope"}), flush=True)
                receipt = cli(binary, connection, "submit", "--input", str(output / batch["path"]))
                batch["receipt_id"] = receipt["id"]
        else:
            batch.update(attempted=True, status="submitting")
            write_json(output / "manifest.json", manifest)
            receipt = cli(binary, connection, "submit", "--input", str(output / batch["path"]))
            batch["receipt_id"] = receipt["id"]
        for _ in range(6):
            if receipt["processing_status"] not in ("pending", "processing"):
                break
            time.sleep(2)
            receipt = cli(binary, connection, "receipt", "--key", batch["key"])
        batch.update(status=receipt["processing_status"], receipt=receipt)
        batch["read_back"] = []
        if batch["status"] in ("completed", "failed"):
            envelope = json.loads((output / batch["path"]).read_text())
            for item, result in zip(envelope["items"], receipt["results"]):
                if result["status"] != "published":
                    continue
                stored = cli(binary, connection, "read", "--memory", result["memory_id"], "--revision", str(result["revision"]))
                memory = stored["memory"]
                expected = f"Source note: {item['title']}\nImported source report; not fresh verification or permission to execute.\n\n{item['text']}"
                if memory["text"] != expected or memory["source_type"] != "external_source_note" or memory["kind"] != "source_note" or memory["status"] != "active" or not stored.get("supporting_evidence"):
                    raise ValueError("published note read-back differs from the prepared source quotation")
                batch["read_back"].append({"memory_id": memory["id"], "revision": memory["revision"], "status": memory["status"], "sha256": digest(memory["text"].encode())})
        write_json(output / "manifest.json", manifest)
        print(json.dumps({"batch": index, "of": len(manifest["batches"]), "status": batch["status"], "results": receipt["results"]}), flush=True)
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workspace", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--binary", default="agent-host")
    parser.add_argument("--connection", default="default")
    parser.add_argument("--submit", action="store_true")
    args = parser.parse_args()
    output = args.output.resolve()
    path = output / "manifest.json"
    manifest = json.loads(path.read_text()) if path.exists() else prepare(args.workspace.resolve(strict=True), output, cli(args.binary, args.connection, "connection", "show")["binding"])
    if Path(manifest["source_workspace"]) != args.workspace.resolve(strict=True):
        raise ValueError("output manifest belongs to another source Workspace")
    if args.submit:
        manifest = submit(args.binary, args.connection, output, manifest)
    print(json.dumps({"manifest": str(path), "files": len(manifest["files"]), "prepared_files": sum(f["status"] == "prepared" for f in manifest["files"]), "excluded_files": sum(f["status"] == "excluded" for f in manifest["files"]), "batches": len(manifest["batches"]), "redactions": sum(f["redactions"] for f in manifest["files"])}))


if __name__ == "__main__":
    main()
