---
name: audit-flagsmith-rollback
description: Audit a Flagsmith rollback ZIP. Check live experiment drift, replay the decision and reconcile a recorded recovery for on-call handoff.
license: MIT
---

Use the operator’s trusted origin, flag and environment. Keep the host-scoped CLI credential and client key (`FLAGSMITH_ENVIRONMENT`) out of output.

From this skill directory, run the read-only audit once. Use the journal inside the ZIP by default. Add `--report "$REPORT"` for a newer same-instance export.

```sh
python3 scripts/audit.py "$CASE" --origin "$ORIGIN" --flag "$FLAG"
```

For a demo in the prepared FlagGuard checkout, run `python3 scripts/rehearse-rollback-skill.py --scenario recovered`. This uses fixture settings and saved Linux polls.

Use the receipt for the handoff. DRIFT compares current settings to rollback. MATCHING_RESET corroborates reopening in the retained journal. Other history outcomes need review. Report missing evidence, identify the next check, and leave changes to the operator. A customer-routing claim needs an application request.
