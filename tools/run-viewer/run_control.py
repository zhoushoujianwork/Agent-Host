"""Control one recorded execution through its owner-private Host capability."""

import http.client
import json
import re

from settings_store import private_bytes


def capability(home, run_id):
    if not isinstance(run_id, str) or not re.fullmatch(r"[a-zA-Z0-9_-]{1,128}", run_id):
        raise ValueError("Invalid run identity")
    value = json.loads(private_bytes(home / (".run-control-" + run_id + ".json"), max_bytes=4096))
    if (not isinstance(value, dict) or value.get("version") != 1 or value.get("run_id") != run_id
            or type(value.get("port")) is not int or not 0 < value["port"] < 65536
            or not isinstance(value.get("token"), str) or not re.fullmatch(r"[a-f0-9]{64}", value["token"])):
        raise ValueError("Invalid run control capability")
    return value


def available(home, run_id):
    try:
        capability(home, run_id)
        return True
    except (OSError, ValueError):
        return False


def stop(home, run_id):
    value = capability(home, run_id)
    connection = http.client.HTTPConnection("127.0.0.1", value["port"], timeout=3)
    try:
        connection.request("POST", "/stop", headers={"X-Run-Token": value["token"]})
        response = connection.getresponse()
        raw = response.read(4097)
        if response.status != 200 or len(raw) > 4096:
            raise ValueError("Host did not accept cancellation; refresh the execution record")
        result = json.loads(raw)
        if not isinstance(result, dict) or result.get("run_id") != run_id or result.get("status") != "stopping":
            raise ValueError("Host cancellation receipt did not match this execution")
    finally:
        connection.close()
