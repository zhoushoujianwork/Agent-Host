"""Fail-closed, viewer-owned restart gate. This never stops a Host service."""

import sqlite3
import threading


class MaintenanceGate:
    def __init__(self):
        # Every HTTP mutation is serialized through this lock, including the
        # idle check, so a submission cannot race the transition to quiescence.
        self.lock = threading.RLock()
        self.quiesced = False

    def begin(self, catalog, chat, purpose="update"):
        if not isinstance(purpose, str) or purpose not in {"update", "connect"}:
            raise ValueError("Unknown maintenance operation")
        with self.lock:
            if chat is None:
                raise ValueError("Workspace activity is unavailable; retry later")
            with chat.lock:
                for runtime in chat.contexts.values():
                    with runtime.lock:
                        if runtime.active or any(child.poll() is None for child in runtime.children.values()):
                            raise ValueError("Wait for tasks and memory processing to finish")
                if purpose == "update":
                    self._sources_idle(catalog)
                self.quiesced = True
            return {"status": "quiesced", "purpose": purpose}

    @staticmethod
    def _sources_idle(catalog):
        sources = getattr(catalog, "catalogs", {"local": catalog})
        if not sources:
            raise ValueError("No authoritative task source is available")
        for source in sources.values():
            try:
                conn = source.connection()
                try:
                    if getattr(conn, "snapshot", False):
                        raise ValueError("Task activity snapshot is stale; retry later")
                    # Query all runs, independently of UI limits or scope. A
                    # final execution result alone does not prove closeout.
                    busy = conn.execute("""SELECT EXISTS(
                        SELECT 1 FROM runs r
                        WHERE COALESCE(json_extract(r.payload, '$.execution_status'), '') NOT IN ('completed', 'failed', 'canceled')
                        OR NOT EXISTS(SELECT 1 FROM events e WHERE e.run_id=r.id
                            AND json_extract(e.payload, '$.type')='run.finished'))""").fetchone()[0]
                    if busy:
                        raise ValueError("Wait for task outcomes and closeout to be recorded")
                finally:
                    conn.close()
            except (OSError, sqlite3.Error):
                raise ValueError("Task activity cannot be verified; retry later") from None

    def resume(self):
        with self.lock:
            self.quiesced = False
            return {"status": "ready"}
