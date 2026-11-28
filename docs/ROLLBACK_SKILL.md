# Audit a checkout rollback

A rollout has reopened since an incident. The [audit skill](../skills/audit-flagsmith-rollback/SKILL.md) checks whether the current settings match a verified reset in FlagGuard’s retained history. It also checks whether the captured policy still reproduces the original rule and decision instant.

## Check an experiment without a FlagGuard installation

Copy the entire `skills/audit-flagsmith-rollback` directory into your agent’s skills directory. This check needs Python 3 and the official Flagsmith CLI v2 on PATH. Use a CLI credential configured for your Flagsmith host, set `FLAGSMITH_ENVIRONMENT` to the target client environment key, and take the experiment ID from your own Flagsmith instance. From the copied skill directory run

```sh
python3 scripts/audit.py --origin "$API_ORIGIN" --flag "$FLAG" --experiment "$EXPERIMENT_ID"
```

The helper reads the environment default and the experiment override with two CLI GETs. It checks that the names and IDs resolve to the same feature. `BOTH_OFF` (exit 0) means both settings were off when read. `SETTING_ENABLED` (exit 1) tells you which setting to reconcile with the change record. `INCONCLUSIVE` (exit 2) means the target or its values could not be verified. To establish what a customer receives, make a fresh application request. To explain *when or why* a setting changed, use the incident workflow below.

On November 28 we ran this check against the running self-hosted checkout experiment. The receipt was `{"flag":"checkout_flow","experiment":1,"default_enabled":false,"rollout_enabled":false,"verdict":"BOTH_OFF"}`. The two full API responses used 485 tokens as compact JSON. The receipt used 28 with the same tokenizer (o200k_base). The complete on-device agent turn used 840 input and 22 output tokens in the runtime’s own accounting. [Command and measured result](evidence/standalone-audit.json)

To try the same workflow without credentials, run `python3 scripts/rehearse-rollback-skill.py --scenario live-override` from the repository with the official CLI v2 installed. The fixture supplies a disabled default and an enabled override. The rehearsal runs a copy of the skill with only the CLI on PATH and checks the two GETs and the resulting handoff.

## Try a handoff

Install Python 3, Go and the [official Flagsmith CLI v2](https://docs.flagsmith.com/integrating-with-flagsmith/CLI). In this repository, build FlagGuard and put it on PATH.

```sh
go build -o bin/flagguard ./cmd/flagguard
export PATH="$PWD/bin:$PATH"
python3 scripts/rehearse-rollback-skill.py --scenario recovered
```

Point a coding agent at `skills/audit-flagsmith-rollback/SKILL.md`, or install the full skill directory using your agent’s skill discovery convention. Ask it to rehearse the recovered checkout and give the on-call handoff. The fixture simulates the current settings and later reset. The CLI and replay binary are real, and the six decision polls came from the Ubuntu checkout trial.

**What to expect.** Both settings are on. Replay confirms the original error decision. The settings differ from rollback (`DRIFT`) and match the later verified reset (`MATCHING_RESET`). A fresh application request is the next check for customer routing.

## Audit an operating environment

1. Open **Incident history**, select the confirmed rollback and download the regression test. For an experiment, new exports include a journal limited to the selected incident and later configuration changes for that flag. Download after recovery to include the reset.
2. Choose the trusted API origin, flag and environment. Configure host-scoped management credentials for the Flagsmith CLI and set `FLAGSMITH_ENVIRONMENT` to the target client environment key. The ZIP cannot identify a trusted target for you.
3. Give the agent the ZIP or run the helper directly. The two CLI calls are projected GET requests. Replay and journal comparison run locally.

```sh
python3 skills/audit-flagsmith-rollback/scripts/audit.py incident.zip \
  --origin "$API_ORIGIN" --flag checkout_flow
```

An extracted case also works. For an older ZIP or a newer same-instance report, append `--report path/to/report.json`. **Export report** in the workspace downloads the report. The tool looks for the exact rollback, then checks subsequent changes for the same flag, feature state and experiment. A matching reset corroborates the settings observed during the two reads. The report cannot establish who approved a change or what happened outside the retained history.

| Receipt | What to do |
| --- | --- |
| `VERIFIED` | Both settings were off when read and replay matched. Verify routing separately if needed. |
| `DRIFT` + `MATCHING_RESET` | Current settings agree with a recorded recovery. Use its timestamp in the handoff. |
| `DRIFT` + `RESET_DIFFERS` or `NO_RETAINED_RESET` | Inspect the change record. A partial reopening can leave the default and override in different states. |
| `LATER_ROLLBACK` or `UNCONFIRMED_CHANGE` | Inspect the newer incident or unresolved change before attributing current state to a reset. |
| `INCIDENT_NOT_IDENTIFIED` or `REPORT_UNAVAILABLE` | Obtain the matching report. The current settings and replay findings are still available. |
| `REPLAY_MISMATCH` | Inspect the policy and trace. Replay reached a different result. |
| `INCONCLUSIVE` | Inspect the missing evidence or ambiguous target. If a setting is enabled, confirmed drift takes precedence over a missing trace. |

The audit exits 0 for verified rollback, 1 for drift or a replay mismatch, and 2 for an inconclusive audit. The rehearsal checks the expected exit status for each case and exits 0 when its assertions pass.

## Run it on-device

On a Mac with macOS 27, the matching Swift toolchain and Apple’s on-device language model available, this optional runner loads the same skill. Its single tool calls the Python audit with arguments chosen by the operator. The model has no shell or write tool. The runner prints the receipt and renders the operational facts directly from its fields.

```sh
swift scripts/on-device-handoff.swift --rehearse
# For an operator-selected target
swift scripts/on-device-handoff.swift incident.zip --origin "$API_ORIGIN" --flag checkout_flow
```

The portable Python audit and coding-agent skill also run on Linux.

## A live check of the ZIP

With the rebuilt controller and a self-hosted Flagsmith instance, a synthetic fault triggered the latency guardrail. Verification took 42.1 seconds. Twenty fresh requests succeeded on control. The first ZIP returned `VERIFIED` with no retained reset. After recovery, a fresh ZIP returned `DRIFT` and `MATCHING_RESET` with the verified reset timestamp. The same p99 decision reproduced in both. Two earlier runs hit local API timeouts, including one unconfirmed rollback that required inspection. [Live receipts](evidence/recovery-handoff.json)

## What the runs cost in tokens

| Measured session | Full input | Cached input included in total | Output |
| --- | ---: | ---: | ---: |
| Apple on-device model, one audit tool, recovered case | 841 | Unreported | 20 |
| Codex CLI, prepared skill, recovered case | 315,434 | 297,088 | 852 |
| Earlier Codex CLI, override-only drift case | 145,527 | 132,864 | 423 |

These are separate runtimes and sessions. The second Codex run inspected the source, handled a sandbox retry and checked temporary files. Its total increased despite the shorter skill. The dedicated local runner exposes only the audit tool to the model. The host handles targeting, execution and the final receipt. [Recovered-case handoffs and client usage](evidence/recovery-handoff.json) · [Earlier live audit and agent session](evidence/rollback-audit.json)

Run `python3 scripts/rehearse-rollback-skill.py` for all 20 cases. Six run from a copied skill directory with only the Flagsmith CLI on PATH. They cover all four combinations of the two settings, a mismatched feature and a missing value. The 14 incident cases cover rollback, reopened override, wrong target, changed policy, missing trace, drift with missing trace, matching recovery, unexplained reopening, differing reset settings, superseding rollback, unrelated report, unconfirmed change, unreadable report and a trace with no eligible samples.
