---
name: audit-flagsmith-rollback
description: Check a self-hosted Flagsmith experiment after a FlagGuard rollback. Use for on-call handoffs, reopened rollouts, or verifying the original decision against recorded traffic.
license: MIT
---

# Audit a rollback

1. Identify the flag, trusted API origin, environment and extracted incident. The export cannot establish its host or environment. Ask for any missing target. Use the configured client environment key in `FLAGSMITH_ENVIRONMENT` and host-scoped CLI credentials. Keep keys out of output.
2. With Python 3, Flagsmith CLI v2 and `flagguard` on PATH, run from this skill's directory:

   ```sh
   python3 scripts/audit.py "$CASE_DIR" --origin "$API_ORIGIN" --flag "$FLAG"
   ```

   Supply a trusted export with `decision.json`, `policy.yaml` and `trace.json`. The helper reads the flag default and experiment override through two CLI GETs, then replays the captured polls locally.

   For a requested **rehearsal**, run `python3 scripts/rehearse-rollback-skill.py --scenario drift` from the FlagGuard repository root. It uses simulated current settings and a recorded Linux incident. Label the result accordingly.
3. Report the flag and both settings. Interpret `same_decision` as the same rule and instant for the recorded variant and experiment. A null value means replay was unavailable.

   | Verdict | Operator handoff |
   | --- | --- |
   | VERIFIED | Both settings were off when read and replay matched. |
   | DRIFT | Identify the enabled setting. Check the recovery or change record before drawing conclusions. |
   | REPLAY_MISMATCH | Both settings were off. Inspect the policy and trace for a different decision. |
   | INCONCLUSIVE | Identify the missing evidence or unresolved target. |

4. Finish with the receipt. These reads establish configuration at inspection time. Customer routing needs a fresh application request. Leave all changes to the operator.
