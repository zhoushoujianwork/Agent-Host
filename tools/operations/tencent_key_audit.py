#!/usr/bin/env python3
"""Read-only CAM inventory with masked keys and atomic resumable checkpoints.

Run with a Python interpreter containing tencentcloud-sdk-python. Credentials
stay in the existing tccli profile; this tool never changes cloud permissions.
"""
import argparse
import concurrent.futures
import datetime
import json
import os
from pathlib import Path
import re
import threading
import time


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def atomic_json(path, value):
    tmp = path.with_suffix(path.suffix + ".tmp")
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        json.dump(value, f, ensure_ascii=False, indent=2)
        f.flush()
        os.fsync(f.fileno())
    os.replace(tmp, path)


class Collector:
    ALLOWED = {
        "GetCallerIdentity", "ListUsers", "ListGroups", "ListAccessKeys",
        "ListGroupsForUser", "ListAttachedUserAllPolicies", "GetPolicy",
        "ListAttachedGroupPolicies", "ListUsersForGroup",
        "GetUserPermissionBoundary",
    }

    def __init__(self, profile, account, output):
        from tencentcloud.common import credential
        from tencentcloud.common.common_client import CommonClient
        from tencentcloud.common.profile.client_profile import ClientProfile
        from tencentcloud.common.profile.http_profile import HttpProfile

        c = json.loads((Path.home() / ".tccli" / (profile + ".credential")).read_text())
        cred = credential.Credential(c["secretId"], c["secretKey"], c.get("token"))
        self.clients = {}
        for svc, version in (("sts", "2018-08-13"), ("cam", "2019-01-16")):
            hp = HttpProfile(endpoint=svc + ".tencentcloudapi.com", reqTimeout=30)
            self.clients[svc] = CommonClient(svc, version, cred, "ap-guangzhou", profile=ClientProfile(httpProfile=hp))
        self.output = Path(output)
        self.output.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.account = str(account)
        self.profile = profile
        self.gate = threading.Lock()
        self.next_call = 0.0

    def call(self, action, **params):
        if action not in self.ALLOWED:
            raise ValueError("Action is not a read-only inventory operation")
        svc = "sts" if action == "GetCallerIdentity" else "cam"
        for attempt in range(5):
            with self.gate:
                time.sleep(max(0, self.next_call - time.monotonic()))
                self.next_call = time.monotonic() + 0.12
            started = now()
            try:
                response = self.clients[svc].call_json(action, params)["Response"]
                if not isinstance(response, dict) or not response.get("RequestId"):
                    raise ValueError("Missing API response identity")
                self.log({"action": action, "at": started, "request_id": response["RequestId"], "ok": True})
                return response
            except Exception as exc:
                # SDK exception text may contain identifiers. Never retain it.
                code = exc.get_code() if hasattr(exc, "get_code") else type(exc).__name__
                code = re.sub(r"[^A-Za-z0-9_.]", "", str(code))[:100]
                self.log({"action": action, "at": started, "ok": False, "code": code})
                if attempt < 4 and any(x in code for x in ("RequestLimitExceeded", "InternalError", "NetworkError")):
                    time.sleep(2 ** attempt)
                    continue
                raise RuntimeError(action + " failed: " + code) from None

    def log(self, data):
        with self.gate:
            path = self.output / "requests.jsonl"
            fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
            with os.fdopen(fd, "w") as f:
                f.write(json.dumps(data) + "\n")

    @staticmethod
    def items(response, key):
        if key not in response or not isinstance(response[key], list):
            raise ValueError("Missing or invalid API list: " + key)
        return response[key]

    def paged(self, action, key, **params):
        result, total = [], None
        for page in range(1, 10001):
            r = self.call(action, Page=page, Rp=200, **params)
            chunk = self.items(r, key)
            count = r.get("TotalNum")
            if not isinstance(count, int) or count < 0 or (total is not None and count != total):
                raise ValueError("Missing or changing pagination total: " + action)
            total = count
            result.extend(chunk)
            if len(result) > total or (not chunk and len(result) < total):
                raise ValueError("Incomplete pagination: " + action)
            if len(result) == total:
                return result
        raise ValueError("Pagination limit exceeded: " + action)

    def group(self, g):
        gid = g["GroupId"]
        return {**g, "policies": self.paged("ListAttachedGroupPolicies", "List", TargetGroupId=gid),
                "members": self.paged("ListUsersForGroup", "UserInfo", GroupId=gid), "checked_at": now()}

    def user(self, u):
        uin = u["Uin"]
        keys = self.items(self.call("ListAccessKeys", TargetUin=uin), "AccessKeys")
        masked = []
        for k in keys:
            kid = k["AccessKeyId"]
            if not isinstance(kid, str) or len(kid) < 10 or k.get("Status") not in ("Active", "Inactive"):
                raise ValueError("Invalid key metadata")
            masked.append({"id_masked": kid[:6] + "****" + kid[-4:], "status": k["Status"],
                           "create_time": k.get("CreateTime")})
        return {"uin": uin, "name": u["Name"], "keys": masked,
                "groups": self.paged("ListGroupsForUser", "GroupInfo", SubUin=uin),
                "policies": self.paged("ListAttachedUserAllPolicies", "PolicyList", TargetUin=uin, AttachType=0),
                "checked_at": now()}

    def collect_phase(self, state, phase, inputs, key, function):
        state.setdefault(phase, {})
        pending = [x for x in inputs if str(key(x)) not in state[phase]]
        failures = {}
        with concurrent.futures.ThreadPoolExecutor(max_workers=3) as pool:
            futures = {pool.submit(function, x): str(key(x)) for x in pending}
            for future in concurrent.futures.as_completed(futures):
                identity = futures[future]
                try:
                    state[phase][identity] = future.result()
                except Exception as exc:
                    failures[identity] = str(exc)[:200]
                state["failures"][phase] = failures.copy()
                atomic_json(self.output / "inventory.json", state)
        if failures:
            raise RuntimeError(phase + ": " + str(len(failures)) + " incomplete items; see checkpoint")
        state["failures"][phase] = {}
        atomic_json(self.output / "inventory.json", state)

    @staticmethod
    def membership_mismatches(state):
        mismatches = {}
        for uin, u in state["users"].items():
            actual = {str(g["GroupId"]) for g in u["groups"]}
            reverse = {gid for gid, g in state["groups"].items() if any(str(m["Uin"]) == uin for m in g["members"])}
            if actual != reverse:
                mismatches[uin] = actual | reverse
        return mismatches

    def reconcile(self, state):
        # Refresh both sides of a mismatch; never silently choose one API view.
        for _ in range(3):
            mismatches = self.membership_mismatches(state)
            if not mismatches:
                return
            for uin in mismatches:
                state["users"][uin] = self.user({"Uin": int(uin), "Name": state["users"][uin]["name"]})
            for gid in set().union(*mismatches.values()):
                state["groups"][gid] = self.group(state["groups"][gid])
            atomic_json(self.output / "inventory.json", state)
        if self.membership_mismatches(state):
            raise ValueError("Group membership remains inconsistent; inventory is incomplete")

    def run(self):
        identity = self.call("GetCallerIdentity")
        if identity.get("AccountId") != self.account:
            raise ValueError("Caller account does not match the requested account")
        path = self.output / "inventory.json"
        if path.exists():
            state = json.loads(path.read_text())
            if state["account"] != self.account or state["profile"] != self.profile:
                raise ValueError("Checkpoint account/profile mismatch")
        else:
            state = {"account": self.account, "profile": self.profile, "started_at": now(), "failures": {}}
        state["identity"] = identity
        state["complete"] = False
        users = self.items(self.call("ListUsers"), "Data")
        groups = self.paged("ListGroups", "GroupInfo")
        if len({u["Uin"] for u in users}) != len(users) or len({g["GroupId"] for g in groups}) != len(groups):
            raise ValueError("Duplicate inventory identities")
        state["user_list"], state["group_list"] = users, groups
        # Drop removed identities, preserve completed items only for this audit.
        for phase, items, field in (("users", users, "Uin"), ("groups", groups, "GroupId")):
            ids = {str(x[field]) for x in items}
            state[phase] = {k: v for k, v in state.get(phase, {}).items() if k in ids}
        atomic_json(path, state)
        self.collect_phase(state, "groups", groups, lambda g: g["GroupId"], self.group)
        self.collect_phase(state, "users", users, lambda u: u["Uin"], self.user)
        self.reconcile(state)
        self.collect_phase(state, "boundaries", users, lambda u: u["Uin"],
                           lambda u: self.call("GetUserPermissionBoundary", TargetUin=u["Uin"]))
        pids = {str(p["PolicyId"]) for g in state["groups"].values() for p in g["policies"]}
        pids.update(str(p["PolicyId"]) for u in state["users"].values() for p in u["policies"])
        self.collect_phase(state, "policies", sorted(pids), lambda p: p,
                           lambda p: self.call("GetPolicy", PolicyId=int(p)))
        final_users = self.items(self.call("ListUsers"), "Data")
        if {u["Uin"] for u in final_users} != {u["Uin"] for u in users}:
            raise ValueError("User inventory changed during collection")
        state["complete"], state["finished_at"] = True, now()
        atomic_json(path, state)
        print(json.dumps({"account": self.account, "users": len(users), "groups": len(groups),
                          "policies": len(pids), "complete": True, "output": str(path)}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile", required=True)
    parser.add_argument("--account", required=True)
    parser.add_argument("--output", required=True, help="Private directory for one audit window; resume the same window only")
    args = parser.parse_args()
    Collector(args.profile, args.account, args.output).run()
