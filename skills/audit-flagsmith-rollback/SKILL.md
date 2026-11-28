---
name: audit-flagsmith-rollback
description: Check whether a Flagsmith experiment is still rolled back. Verify current settings with the CLI or replay a FlagGuard incident and explain a recovery.
license: MIT
---

Use the operator’s trusted origin, flag and environment. Keep the host-scoped CLI credential and client key (`FLAGSMITH_ENVIRONMENT`) out of output. From this skill directory choose one read-only check.

- Have a FlagGuard export? Run `python3 scripts/audit.py "$CASE" --origin "$ORIGIN" --flag "$FLAG"`. Replay needs the FlagGuard binary. The ZIP includes the journal when available. Use `--report "$REPORT"` for a newer same-instance export.
- Have only the experiment ID? Run `python3 scripts/audit.py --experiment "$ID" --origin "$ORIGIN" --flag "$FLAG"`. This needs Python 3 and the Flagsmith CLI v2. Report the observed default and experiment override. If either is enabled, check the operator’s change record.

Use the receipt for the handoff. DRIFT compares settings to rollback. MATCHING_RESET corroborates reopening. State any missing evidence. A customer-routing claim needs a fresh application request. Leave changes to the operator.
