# Audit a Flagsmith experiment rollback

An operator sees a disabled flag the morning after a checkout incident. Is the experiment override also off? Would the saved readings still produce the rule and decision time in the incident report?

The [Agent Skill](../skills/audit-flagsmith-rollback/SKILL.md) answers both questions. The flags and experiment are identified by name and ID. The Flagsmith CLI reads current settings, then FlagGuard replays the captured policy. The agent returns a short receipt that can be pasted into the incident handoff. Neither tool changes the rollout during an audit.

## Try the skill

Install Python 3 and the [official Flagsmith CLI v2](https://docs.flagsmith.com/integrating-with-flagsmith/CLI). From the root of this repository, build the current FlagGuard binary and put it on your PATH:

```sh
go build -o bin/flagguard ./cmd/flagguard
export PATH="$PWD/bin:$PATH"
python3 scripts/rehearse-rollback-skill.py
```

Point your coding agent at `skills/audit-flagsmith-rollback/SKILL.md`, or install that whole directory using the agent's skill discovery convention. For a short demo, use this prompt:

> Use the audit-flagsmith-rollback skill. Rehearse the drift case and give me a two-sentence incident handoff. Identify which setting changed, whether the original decision reproduced, and what I should check next.

The rehearsal runs locally with dummy credentials. It uses the actual CLI, a loopback API fixture representing current settings, the FlagGuard binary and the [recorded Ubuntu incident](evidence/ubuntu-checkout/decision.json). Six cases were exercised with Flagsmith CLI v2.0.0:

| Scenario | Observation | Receipt |
| --- | --- | --- |
| Verified rollback | Both settings off, original error rule reproduced at the recorded instant | VERIFIED |
| Rollout drift | Default off, experiment override on, original decision reproduced | DRIFT |
| Wrong target | Experiment refers to a different feature ID | INCONCLUSIVE |
| Changed policy | Both settings off, a longer error confirmation makes latency fire instead | REPLAY_MISMATCH |
| Missing trace | Both settings off, historical decision unavailable | INCONCLUSIVE |
| Missing trace with drift | Override on, historical decision unavailable | DRIFT |

The drift handoff should call out the enabled experiment override and recommend checking for an intentional recovery. It cannot identify who changed the setting or prove what a customer saw. For a single example, append `--scenario drift` to the rehearsal command. The rehearsal exits successfully when each selected scenario yields its expected result.

## Audit an operating environment

Supply a trusted, extracted FlagGuard incident export with recorded polls. Choose the target and flag explicitly. Configure the CLI's read credentials for that specific host using its [static credential instructions](https://docs.flagsmith.com/integrating-with-flagsmith/CLI). Set `FLAGSMITH_ENVIRONMENT` to its client-side environment key. For the bundled local lab the API origin is `http://localhost:8000`. Use your own environment's HTTPS origin for a remote deployment.

```sh
python3 skills/audit-flagsmith-rollback/scripts/audit.py "$CASE_DIR" \
  --origin "$API_ORIGIN" --flag checkout_flow
```

The audit exits 0 for a verified rollback, 1 for drift or a different replay decision, and 2 when it cannot verify the target or evidence. The two reads are sequential observations of current configuration. A later intentional reopening can produce drift even when the original rollback succeeded.

## Measured runs

On November 28, the official CLI read a running self-hosted Flagsmith instance after a synthetic checkout fault. FlagGuard observed the verified rollback after 40.1 seconds. All 20 follow-up checkouts succeeded on control. The audit returned **VERIFIED** with both settings off. After an intentional recovery, it returned **DRIFT** with both settings on. The same exported decision reproduced the p99 latency rule at its original instant in both audits.

In a separate Codex session, the agent loaded the skill, ran the drift rehearsal and correctly identified that only the experiment override was on. It recommended checking the recovery record and verifying routing with a fresh request. The CLI projected two GET responses and the helper returned a 40-token receipt. Measured with `o200k_base`, the skill itself was 417 tokens, including the trailing newline.

| Full Codex turn, reported by the client | Tokens |
| --- | ---: |
| Input across the turn | 145,527 |
| Cached input, included above | 132,864 |
| Output | 423 |

The full turn included the coding agent’s instructions, tool definitions and source inspection. These figures describe one run. [Live receipts, agent handoff and usage record](evidence/rollback-audit.json)
