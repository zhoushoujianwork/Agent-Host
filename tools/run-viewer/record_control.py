"""Authorize record-scoped control without changing the shared Chat selection."""

import http.client

import run_control


class RecordControlConflict(ValueError):
    pass


def resolve(catalog, key):
    # Read through the public catalog first: its scope checks also apply to stop.
    record = catalog.record(key)
    source, reference = catalog, key
    if hasattr(catalog, "catalogs"):
        name, _, reference = key.partition(":")
        source = catalog.catalogs[name]
    return record, source, reference


def describe(record, source, reference):
    if record.get("snapshot") or not reference.startswith("db:"):
        return {"active": False, "can_stop": False, "reason": "Archived snapshots cannot be controlled."}
    run = record["run"]
    finished = any(event.get("type") == "run.finished" for event in record["events"])
    available = not finished and run_control.available(source.database.parent, run["id"])
    active = not finished and (run["execution_status"] in {"starting", "running", "stopping"} or available)
    return {"active": active, "can_stop": bool(active and available),
            "reason": "This execution has finished." if finished else
                      "This Host does not provide run control; stop at the original entry." if active and not available else ""}


def record(catalog, key):
    value, source, reference = resolve(catalog, key)
    return {**value, "control": describe(value, source, reference)}


def stop(catalog, key):
    value, source, reference = resolve(catalog, key)
    control = describe(value, source, reference)
    if not control["can_stop"]:
        raise RecordControlConflict(control["reason"] or "This execution cannot be stopped; refresh its record.")
    try:
        run_control.stop(source.database.parent, value["run"]["id"])
    except (OSError, ValueError, http.client.HTTPException) as error:
        raise RecordControlConflict("Cancellation could not be confirmed; refresh the record before retrying.") from error
    return {"key": key, "run_id": value["run"]["id"], "status": "stopping"}
