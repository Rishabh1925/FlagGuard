#!/usr/bin/env python3
"""Exercise the actual CLIs on a recorded incident and a loopback Flagsmith API fixture."""
import argparse
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import threading
from zipfile import ZipFile

ROOT = Path(__file__).resolve().parents[1]
FLAG = "checkout_flow"
CASES = ROOT / "docs/evidence/ubuntu-checkout"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--scenario", choices=("verified", "drift", "wrong-target", "changed-policy",
                                                "missing-trace", "drift-missing-trace", "recovered", "unexplained",
                                                "different-settings", "superseded", "unrelated-report",
                                                "unconfirmed", "invalid-report", "no-samples",
                                                "live-off", "live-default", "live-override", "live-reopened",
                                                "live-wrong-feature", "live-missing-value"),
                        help="Run one scenario. By default, verify every scenario.")
    scenario = parser.parse_args().scenario
    with tempfile.TemporaryDirectory() as temp:
        case = Path(temp) / "incident"
        shutil.copytree(CASES, case)
        current = {"enabled": False, "default": False, "feature_id": 7}
        calls = []

        class Fixture(BaseHTTPRequestHandler):
            def do_GET(self):
                calls.append(self.path)
                if self.path == "/api/v1/environments/trial/featurestates/?page_size=100&feature_name=checkout_flow":
                    body = {"next": None, "results": [{"id": 1, "feature": {"id": 7, "name": FLAG}, "enabled": current["default"]}]}
                elif self.path == "/api/v1/environments/trial/experiments/1/":
                    body = {"id": 1, "status": "running", "feature": {"id": current["feature_id"], "name": FLAG},
                            "experiment_rollout": {"enabled": current["enabled"]}}
                else:
                    self.send_error(404)
                    return
                if not self.headers.get("Authorization"):
                    self.send_error(401)
                    return
                data = json.dumps(body).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def log_message(self, *_):
                pass

        with HTTPServer(("127.0.0.1", 0), Fixture) as server:
            threading.Thread(target=server.serve_forever, daemon=True).start()
            port = server.server_port
            env = {"HOME": temp, "PATH": os.environ["PATH"], "FLAGSMITH_ENVIRONMENT": "trial",
                   f"FLAGSMITH_API_KEY_127_0_0_1_{port}": "fixture.only"}
            def check(name, expected, code, same_decision=None, history=None):
                if scenario and name != scenario:
                    return
                calls.clear()
                source = case
                options = []
                if history:
                    source = Path(temp) / "incident.zip"
                    with ZipFile(source, "w") as archive:
                        for member in ("decision.json", "trace.json", "policy.yaml"):
                            archive.write(case / member, member)
                        if name == "recovered":
                            archive.write(report_path, "report.json")
                    if name != "recovered":
                        options = ["--report", str(report_path)]
                result = subprocess.run([sys.executable, str(ROOT / "skills/audit-flagsmith-rollback/scripts/audit.py"),
                                         str(source), "--origin", f"http://127.0.0.1:{port}", "--flag", FLAG, *options],
                                        env=env, cwd=temp, capture_output=True, text=True, timeout=45)
                receipt = json.loads(result.stdout)
                if (result.returncode != code or receipt["verdict"] != expected or len(calls) != 2
                        or receipt.get("same_decision") is not same_decision or receipt.get("history") != history):
                    raise RuntimeError(f"Expected {expected}, received {receipt}, GET count {len(calls)}")
                print(result.stdout.strip())
            # Execute a copied skill with only the real Flagsmith CLI on PATH.
            standalone = Path(temp) / "standalone"
            shutil.copytree(ROOT / "skills/audit-flagsmith-rollback", standalone)
            cli_dir = standalone / "bin"
            cli_dir.mkdir()
            cli = shutil.which("flagsmith")
            if not cli:
                raise RuntimeError("Install the official Flagsmith CLI v2 on PATH")
            (cli_dir / "flagsmith").symlink_to(cli)
            def check_live(name, default, override, feature, expected, code):
                if scenario and name != scenario:
                    return
                current.update(default=default, enabled=override, feature_id=feature)
                calls.clear()
                result = subprocess.run([sys.executable, "scripts/audit.py", "--experiment", "1", "--flag", FLAG,
                                         "--origin", f"http://127.0.0.1:{port}"], cwd=standalone,
                                        env={**env, "PATH": str(cli_dir)}, capture_output=True, text=True, timeout=45)
                receipt = json.loads(result.stdout)
                if result.returncode != code or len(calls) != 2 or receipt["verdict"] != expected:
                    raise RuntimeError(f"{name}: {receipt}, exit {result.returncode}, GET count {len(calls)}")
                if code != 2 and (receipt["default_enabled"] is not default or receipt["rollout_enabled"] is not override):
                    raise RuntimeError(f"{name}: wrong settings in {receipt}")
                print(result.stdout.strip())
            try:
                check_live("live-off", False, False, 7, "BOTH_OFF", 0)
                check_live("live-default", True, False, 7, "SETTING_ENABLED", 1)
                check_live("live-override", False, True, 7, "SETTING_ENABLED", 1)
                check_live("live-reopened", True, True, 7, "SETTING_ENABLED", 1)
                check_live("live-wrong-feature", False, False, 8, "INCONCLUSIVE", 2)
                check_live("live-missing-value", None, False, 7, "INCONCLUSIVE", 2)
                current.update(default=False, enabled=False, feature_id=7)
                check("verified", "VERIFIED", 0, True)
                current["enabled"] = True
                check("drift", "DRIFT", 1, True)
                current["enabled"] = False
                current["feature_id"] = 8
                check("wrong-target", "INCONCLUSIVE", 2)
                current["feature_id"] = 7
                incident = json.loads((case / "decision.json").read_bytes())
                reset = {"kind": "RESET", "flag": FLAG, "time": "2026-11-23T20:22:00Z",
                         "change": {**incident["change"], "default_after_enabled": True,
                                    "rollout_after_enabled": True, "after_enabled": True}}
                report_path = Path(temp) / "report.json"
                def journal(*events):
                    report_path.write_text(json.dumps({"schema_version": 1, "generated_at": "2026-11-23T20:25:00Z",
                                                      "events": list(events)}))

                current["enabled"] = True
                journal(incident)
                check("unexplained", "DRIFT", 1, True, "NO_RETAINED_RESET")
                journal(incident, reset)
                check("different-settings", "DRIFT", 1, True, "RESET_DIFFERS")
                current["default"] = True
                check("recovered", "DRIFT", 1, True, "MATCHING_RESET")
                journal(incident, reset, {**incident, "time": "2026-11-23T20:23:00Z"})
                check("superseded", "DRIFT", 1, True, "LATER_ROLLBACK")
                journal({**incident, "variant": "other"}, reset)
                check("unrelated-report", "DRIFT", 1, True, "INCIDENT_NOT_IDENTIFIED")
                journal(incident, {**reset, "kind": "RECOVERY_UNCONFIRMED"})
                check("unconfirmed", "DRIFT", 1, True, "UNCONFIRMED_CHANGE")
                report_path.write_text("incomplete")
                check("invalid-report", "DRIFT", 1, True, "REPORT_UNAVAILABLE")
                current.update(enabled=False, default=False)
                policy_path = case / "policy.yaml"
                policy = json.loads(policy_path.read_text())
                policy["guardrails"][0]["rules"][0]["for"] = "1m"
                policy_path.write_text(json.dumps(policy))
                check("changed-policy", "REPLAY_MISMATCH", 1, False)
                trace_path = case / "trace.json"
                trace = json.loads(trace_path.read_bytes())
                for series in trace["series"].values():
                    series[:] = [None] * len(series)
                trace_path.write_text(json.dumps(trace))
                check("no-samples", "INCONCLUSIVE", 2)
                trace_path.unlink()
                check("missing-trace", "INCONCLUSIVE", 2)
                current["enabled"] = True
                check("drift-missing-trace", "DRIFT", 1)
            finally:
                server.shutdown()


if __name__ == "__main__":
    main()
