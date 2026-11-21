# Try FlagGuard in five minutes

**What it does:** watches the reliability of a Flagsmith experiment and disables an unhealthy rollout. It records the measurements and verifies the changed flag settings, so an operator can explain the decision afterwards.

**Why it matters:** an experiment can look promising while making checkout slower or less reliable. FlagGuard gives self-hosted teams a reusable reliability guardrail with a rehearsal mode and inspectable incident evidence.

[Watch the full browser walkthrough](flagguard-demo-silent.mp4) with the [timed script](VOICEOVER.md), or reproduce the incident below.

## Start

Install Docker with Compose and Python 3. From the repository, run `make demo` (first startup downloads images and can take several minutes). See [fresh-machine instructions](FRESH_MACHINE.md) if you do not have Make or use Windows.

Open **http://localhost:8085**. Try **Incident replay** to inspect the recorded 39.6-second rollback. In **Policy bench**, move the error limit to 30% and observe which measurement still exceeds its limit. These sliders compare a recorded sample window locally. Select **Open workspace** to operate the running demo. The Overview puts live guardrails and the Demo lab together. Wait for **HEALTHY** before starting. This is a local synthetic checkout demonstration; it does not represent real shoppers or a conversion experiment result.

## Watch one incident

1. **Establish the baseline.** Two checkout versions receive synthetic traffic. “Control” is the existing version; “Variant B” is the experimental version. Error rate measures failed requests. P99 latency is the approximate duration below which 99% of requests finish.
2. **Inject a fault.** In Overview's Demo lab, select **Inject fault into Variant B**. Only the experimental checkout is deliberately degraded. Keep the guardrail readings visible.
3. **Watch confirmation.** A threshold breach must last 15 seconds, with sufficient traffic. The metric window is 30 seconds and evaluations occur every 5 seconds, so rollback is not instantaneous. Control readings give context; they do not prove causation or veto a breached treatment limit.
4. **Inspect the rollback.** The state becomes **ROLLED_BACK**. In Incident history, open the incident and export its report. It includes the observed measurements, limits, timing, control comparison, and verified changes to both the default flag and the experiment rollout override.
5. **Recover deliberately.** Select **Review recovery**, inspect the warning, and confirm. The demo fault stops; the rollout is enabled and waits for fresh measurements before returning to HEALTHY. The original incident evidence remains available.

For an automated proof of this sequence, run `python3 scripts/smoke.py`. It additionally checks that 20 subsequent demo checkout responses use control before recovery. The output is saved to `tmp/smoke-result.json`.

## What to evaluate

| Question | Evidence to inspect |
| --- | --- |
| Is Flagsmith essential? | Real self-hosted flag and experiment override changes, followed by API read-back |
| Is the decision explainable? | Frozen rule values, sample gate, confirmation timing, and control context in the incident report |
| Can a team rehearse first? | `mode: shadow` produces a proposed rollback without changing Flagsmith |
| Can someone else reuse it? | Configurable Prometheus queries, standalone/production Compose files, preflight command, and operations guide |
| What happens when a write is uncertain? | ACTION_REQUIRED locks further automatic action until operator review; state survives restart |

## Scope and honesty

This is reliability protection, not Flagsmith's statistical winner-selection engine. API read-back proves configuration changes; client propagation must be verified separately, as the demo smoke test does. Production deployment targets one controller with durable local storage, authentication, and an HTTPS proxy. Multi-host failover, per-user roles, arbitrary targeting overrides, and guaranteed webhook delivery are outside the current scope. See [verification status](VERIFICATION.md) and [operations](OPERATIONS.md) for completed checks and remaining release gates.
