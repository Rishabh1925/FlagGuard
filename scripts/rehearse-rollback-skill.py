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

ROOT = Path(__file__).resolve().parents[1]
FLAG = "checkout_flow"
CASES = ROOT / "docs/evidence/ubuntu-checkout"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--scenario", choices=("verified", "drift", "wrong-target", "changed-policy",
                                                "missing-trace", "drift-missing-trace"),
                        help="Run one scenario. By default, verify every scenario.")
    scenario = parser.parse_args().scenario
    with tempfile.TemporaryDirectory() as temp:
        case = Path(temp) / "incident"
        shutil.copytree(CASES, case)
        current = {"enabled": False, "feature_id": 7}
        calls = []

        class Fixture(BaseHTTPRequestHandler):
            def do_GET(self):
                calls.append(self.path)
                if self.path == "/api/v1/environments/trial/featurestates/?page_size=100&feature_name=checkout_flow":
                    body = {"next": None, "results": [{"id": 1, "feature": {"id": 7, "name": FLAG}, "enabled": False}]}
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
            def check(name, expected, code, same_decision=None):
                if scenario and name != scenario:
                    return
                calls.clear()
                result = subprocess.run([sys.executable, str(ROOT / "skills/audit-flagsmith-rollback/scripts/audit.py"),
                                         str(case), "--origin", f"http://127.0.0.1:{port}", "--flag", FLAG],
                                        env=env, cwd=temp, capture_output=True, text=True, timeout=45)
                receipt = json.loads(result.stdout)
                if (result.returncode != code or receipt["verdict"] != expected or len(calls) != 2
                        or receipt.get("same_decision") is not same_decision):
                    raise RuntimeError(f"Expected {expected}, received {receipt}, GET count {len(calls)}")
                print(result.stdout.strip())
            try:
                check("verified", "VERIFIED", 0, True)
                current["enabled"] = True
                check("drift", "DRIFT", 1, True)
                current["enabled"] = False
                current["feature_id"] = 8
                check("wrong-target", "INCONCLUSIVE", 2)
                current["feature_id"] = 7
                policy_path = case / "policy.yaml"
                policy = json.loads(policy_path.read_text())
                policy["guardrails"][0]["rules"][0]["for"] = "1m"
                policy_path.write_text(json.dumps(policy))
                check("changed-policy", "REPLAY_MISMATCH", 1, False)
                (case / "trace.json").unlink()
                check("missing-trace", "INCONCLUSIVE", 2)
                current["enabled"] = True
                check("drift-missing-trace", "DRIFT", 1)
            finally:
                server.shutdown()


if __name__ == "__main__":
    main()
