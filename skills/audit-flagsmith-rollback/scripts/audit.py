#!/usr/bin/env python3
"""Check a Flagsmith experiment rollback, optionally replaying its recorded incident."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
from urllib.parse import quote, urlsplit
from zipfile import BadZipFile

sys.dont_write_bytecode = True
from incident import incident_directory, instant, recovery_history


def run_json(args):
    try:
        result = subprocess.run(args, capture_output=True, timeout=30, check=True)
    except (OSError, subprocess.SubprocessError) as exc:
        # CLI errors may include environment identifiers or authentication headers.
        raise ValueError(f"{Path(args[0]).name} failed; check CLI version, access and target") from exc
    return json.loads(result.stdout)


def api(origin, path, projection):
    return run_json(["flagsmith", "api", "api/v1/" + path, "--method", "GET",
                     "--api-url", origin, "--no-input", "--jq", projection])


def positive_id(value):
    return type(value) is int and value > 0


def read_settings(origin, environment, flag, experiment, state_id=None):
    url = urlsplit(origin)
    if (not url.hostname or url.username is not None or url.password is not None
            or url.query or url.fragment or url.path not in ("", "/")
            or not (url.scheme == "https" or (url.scheme == "http" and url.hostname in ("localhost", "127.0.0.1", "::1")))):
        raise ValueError("supply a trusted HTTPS origin or a loopback HTTP origin without an API path")
    if not re.fullmatch(r"[A-Za-z0-9_-]+", environment):
        raise ValueError("supply the target's client-side environment key")
    if not positive_id(experiment) or not flag.strip():
        raise ValueError("supply a positive experiment ID and a flag name")
    root = f"environments/{environment}/"
    states = api(origin, root + "featurestates/?page_size=100&feature_name=" + quote(flag, safe=""),
                 '{next:(if type=="array" then null else .next end),'
                 'states:[(if type=="array" then .[] else .results[] end)'
                 '|select(.identity==null and .feature_segment==null)'
                 '|{id,enabled,feature_id:(.feature|if type=="object" then .id else . end),'
                 'name:(.feature|if type=="object" then .name else null end)}]}')
    if states["next"] or len(states["states"]) != 1:
        raise ValueError("target lookup was incomplete or ambiguous")
    state = states["states"][0]
    rollout = api(origin, root + f"experiments/{experiment}/",
                  '{id,status,feature_id:.feature.id,name:.feature.name,enabled:.experiment_rollout.enabled}')
    if (not positive_id(state["id"]) or (state_id is not None and state["id"] != state_id)
            or not positive_id(state["feature_id"])
            or not positive_id(rollout["id"]) or rollout["id"] != experiment
            or not positive_id(rollout["feature_id"]) or rollout["feature_id"] != state["feature_id"]
            or (state_id is None and flag not in (state["name"], rollout["name"]))
            or any(name not in (None, flag) for name in (state["name"], rollout["name"]))
            or rollout["status"] not in ("paused", "running")
            or any(type(row["enabled"]) is not bool for row in (state, rollout))):
        raise ValueError("could not verify the settings for the requested flag and experiment")
    return {"flag": flag, "experiment": experiment, "default_enabled": state["enabled"],
            "rollout_enabled": rollout["enabled"]}


def inspect_live(origin, environment, flag, experiment):
    receipt = read_settings(origin, environment, flag, experiment)
    enabled = receipt["default_enabled"] or receipt["rollout_enabled"]
    return dict(receipt, verdict="SETTING_ENABLED" if enabled else "BOTH_OFF")


def audit(case, origin, environment, flag, report=None):
    event = json.loads((case / "decision.json").read_text())
    change = event["change"]
    experiment = change.get("experiment_id")
    if (event["flag"] != flag or event["kind"] != "ROLLBACK"
            or not positive_id(change["feature_state_id"]) or not positive_id(experiment)
            or change.get("default_after_enabled") is not False or change.get("rollout_after_enabled") is not False):
        raise ValueError("expected a confirmed experiment rollback for the requested flag")
    receipt = read_settings(origin, environment, flag, experiment, change["feature_state_id"])
    receipt["recorded_rule"] = event["metric"]
    if report is not None:
        receipt.update(recovery_history(report, event, receipt))
    drift = receipt["default_enabled"] or receipt["rollout_enabled"]
    try:
        replay = run_json(["flagguard", "-config", str(case / "policy.yaml"), "-replay",
                           str(case / "trace.json"), "-format", "json"])
        rows = [r for r in replay["guardrails"] if r["flag"] == flag and r["variant"] == event["variant"]]
        if len(rows) != 1:
            raise ValueError("flag or variant missing from replay")
        if rows[0]["outcome"] == "insufficient_data":
            raise ValueError("replay has no eligible polls")
        decision = rows[0].get("first_decision") or {}
        same = (rows[0]["outcome"] == "would_roll_back" and decision.get("metric") == event["metric"]
                and instant(decision["time"]) == instant(event["time"])
                and (decision.get("evidence") or {}).get("experiment_id") == experiment)
    except (OSError, ValueError, TypeError, KeyError, AttributeError):
        return dict(receipt, same_decision=None, verdict="DRIFT" if drift else "INCONCLUSIVE",
                    detail="Historical replay unavailable; check the export, policy and FlagGuard binary")
    return dict(receipt, same_decision=same,
                verdict="DRIFT" if drift else "VERIFIED" if same else "REPLAY_MISMATCH")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("case", type=Path, nargs="?")
    source.add_argument("--experiment", type=int, help="Inspect current settings without a FlagGuard incident")
    parser.add_argument("--origin", required=True)
    parser.add_argument("--flag", required=True)
    parser.add_argument("--report", type=Path, help="Correlate a saved report from this FlagGuard instance")
    args = parser.parse_args()
    try:
        environment = os.environ["FLAGSMITH_ENVIRONMENT"]
        if args.experiment is not None:
            if args.report is not None:
                parser.error("--report needs an incident")
            receipt = inspect_live(args.origin, environment, args.flag, args.experiment)
        else:
            with incident_directory(args.case) as case:
                report = args.report
                if report is None and (case / "report.json").is_file():
                    report = case / "report.json"
                receipt = audit(case, args.origin, environment, args.flag, report)
    except (OSError, ValueError, KeyError, TypeError, IndexError, AttributeError, BadZipFile, RuntimeError) as error:
        print(json.dumps({"verdict": "INCONCLUSIVE", "detail": str(error)}))
        sys.exit(2)
    print(json.dumps(receipt, separators=(",", ":")))
    sys.exit({"VERIFIED": 0, "BOTH_OFF": 0, "INCONCLUSIVE": 2}.get(receipt["verdict"], 1))
