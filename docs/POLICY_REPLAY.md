# Test a policy against a past incident

A team changing a latency limit can use the same checkout incident as a regression test on every pull request. FlagGuard runs the controller's shadow evaluation loop over a saved metric window. The replay needs the policy and trace files. It uses no Flagsmith credentials, local controller state, webhook, or running services.

## Turn a retained rollback into a test

Open **Workspace → Incident history**, select the rollback and click **Download regression test**. Confirmed rollbacks and shadow decisions with a recorded breach start support export. FlagGuard saves the readings and actual poll timestamps for a continuous breach (up to 256 polls including the preceding healthy poll when available). It replays those readings with the recorded policy and verifies the first triggering rule and decision time. Older decisions and longer episodes use a fixed-grid reconstruction from Prometheus. If that reconstruction disagrees, the export reports the recorded and replayed outcomes and returns HTTP 409.

Extract the ZIP and run this command in that directory with a FlagGuard binary on your path.

```sh
flagguard -config policy.yaml -suite suite.json -format text
```

Edit a copy of the policy and run the same suite against it. Each exported case checks the rule and its one-based `decision_poll`. A change that triggers earlier or misses the original deadline fails the case. Teams can review intentional changes to this expectation in the same pull request as the policy.

The capture ends at the original decision. A missed deadline tells you the flag was still armed at that poll. Later traffic is outside the test. The ZIP includes query text, labels and the selected decision, which may identify internal services.

To reproduce the pricing example from source, run `go test ./internal/server -run TestRecordedIncidentDownload -v`. It exercises the download handler with the archived decision and its original Prometheus timestamps, then tests both an early rollback and a missed rollback.

## Compare the trigger and the deadline

An incident export includes two test manifests. `suite.json` requires the same first rule at the same poll. `protection.json` accepts any rule on the named flag that triggers by the deadline, including an earlier rollback. Check both when reviewing policy changes. A deadline check alone allows an overly sensitive policy, so include healthy and transient cases when assessing false alarms.

To reproduce the Ubuntu checkout trial from the repository root, run `make replay-ubuntu`. For the one-minute proposal, create a local copy of the exported policy and run both checks again:

```sh
python3 - <<'PYTHON'
import json
from pathlib import Path
policy = json.loads(Path("docs/evidence/ubuntu-checkout/policy.yaml").read_text())
rule = next(r for r in policy["guardrails"][0]["rules"] if r["metric"] == "error_rate")
rule["for"] = "1m"
Path("tmp").mkdir(exist_ok=True)
Path("tmp/ubuntu-one-minute.yaml").write_text(json.dumps(policy, indent=2))
PYTHON
make replay-ubuntu UBUNTU_POLICY=tmp/ubuntu-one-minute.yaml
```

On the supplied trace the first command in the make target passes the deadline on latency at poll 6. The second exits nonzero because the recorded decision used the error rate. For another service, specify its flag and one-based `rollback_by_poll` in a suite case. The poll must lie within the capture. [Example deadline manifest](evidence/ubuntu-checkout/protection.json)

## Try the incident from the video

With Go and Make installed, run `make replay` in the repository root. The first build downloads Go dependencies. The bundled [trace](evidence/checkout-trace.json) comes from local Prometheus during the filmed synthetic checkout incident. It contains 25 evaluations at five-second intervals. The window ends in the second the live controller made its rollback decision.

To compare thresholds, copy `guardrails.yaml` to a temporary candidate file and change only its thresholds. Run this for each version, substituting its path for `candidate.yaml`:

```sh
go run ./cmd/flagguard -config candidate.yaml -replay docs/evidence/checkout-trace.json -format text
```

| Error limit | P99 limit | Result on the same two minutes of traffic |
| --- | --- | --- |
| 5% | 500 ms | The error-rate rule would trigger at 17:48:37 UTC. |
| 30% | 500 ms | The latency rule would trigger at the same evaluation. |
| 30% | 1,000 ms | Neither rule would trigger within the captured window. All 25 polls had enough treatment data. |

Both shipped rules had completed their timers at the final replay poll. The first eligible rule in YAML order supplies the reported trigger, just as it does in the live controller. The replay reads 25.6% errors on its fixed five-second grid. The original video inspector reads 26.2% from the live evaluation. The live run predates aligned query timestamps. Timing, data available at evaluation, and missed polls can change a historical result.

## Capture your own window

Point `PROMETHEUS_URL` at your Prometheus instance and supply the policy used for this service. All timestamps include a time zone. Omit `-until` to end at the current time.

```sh
go run ./cmd/flagguard -config guardrails.yaml -backtest 10m \
  -until 2026-11-21T17:48:37Z -save-trace incident.json -format text
```

The command calls Prometheus's range query API once per distinct configured query, then evaluates those results locally. The window must be a multiple of the policy's poll interval and fit within 2,000 evaluations. A trace records query text, UTC start, cadence, and numeric readings. Missing or non-finite readings are stored as null. Review query labels and measurements before sharing them. The output trace path must be new.

A saved trace supports changes to limits, confirmation periods, rule order and sample gates. Keep the original cadence and use queries present in the capture. A different query or cadence needs another capture. For comparable live and historical results, use rolling queries with evaluation-relative windows. Range-wide PromQL modifiers such as `@ start()` and `@ end()` have different semantics across a range and an instant query.

## Run a regression check

```sh
go run ./cmd/flagguard -config guardrails.yaml \
  -replay docs/evidence/checkout-trace.json -expect checkout_flow=error_rate
```

The default output is JSON. `-expect` exits nonzero if the first trigger differs, the named flag is missing, or the expected trigger never occurs. Use `flag=none` to require zero triggers **and** sufficient treatment measurements at every scheduled poll. Missing control data limits diagnosis but leaves the absolute treatment limits active.

The [composite GitHub Action](../action.yml) exposes the same check as a pull-request step. This example runs from a checkout containing the action. In another repository, reference the action by its published commit when available and pass paths to that repository's policy and trace.

```yaml
- uses: actions/checkout@v4
- uses: ./
  with:
    policy: guardrails.yaml
    trace: docs/evidence/checkout-trace.json
    expect: checkout_flow=error_rate
```

Inspect the result in the job summary. Replay starts with an enabled flag and fresh timers and stops at the first decision for each guardrail. The replay does not reconstruct historical operator changes, API failures or traffic that would have followed a different decision. The live smoke test checks actual writes and application routing separately.

## Test a policy against several incidents

Keep a JSON manifest beside your traces. Point every case at a trace within that directory and give its expected flag and first trigger. A `none` expectation requires sufficient treatment data throughout the window. A trace with missing readings cannot pass as a healthy baseline.

```json
{"cases": [
  {"name": "Healthy prices", "trace": "healthy.json", "expect": "price_response_v2=none"},
  {"name": "Broken response", "trace": "sustained.json", "expect": "price_response_v2=contract_pass_fraction"}
]}
```

Run `make test-price-policy` for the shipped example, or run:

```sh
go run ./cmd/flagguard -config deploy/blackbox/guardrails.yaml \
  -suite deploy/blackbox/cases/suite.json -format text
```

The command checks every case, prints mismatches, and exits nonzero if any failed. Omit `-format text` for JSON with the observed trigger, eligible poll counts and decision times. In a pull request, use the composite action with `policy` and `suite` inputs. [See the working CI step](../.github/workflows/ci.yml).

## Test a detection deadline with missing readings

A policy can catch every recorded failure under ideal monitoring and still be fragile during a telemetry outage. Run `make stress-price` to inspect the incident in the price example. The tool finds the first rule to trip, then replays the full trace once for each evaluation in that rule’s breach confirmation interval. In each replay, every query for the selected flag returns no data at exactly one poll. The controller still runs at that poll and applies its normal missing-data behavior. Other rules protecting the same flag can still trip on subsequent polls.

| Missing evaluation, from trace start | Decision with the shipped 15-second confirmation |
| --- | --- |
| None | 45 s |
| 30 s | 50 s |
| 35 s | 55 s |
| 40 s | 60 s |
| 45 s | No decision by the end of the 60-second capture |

To turn a detection deadline into a release check, run:

```sh
go run ./cmd/flagguard -config deploy/blackbox/guardrails.yaml \
  -replay deploy/blackbox/cases/sustained.json -gap-check price_response_v2 \
  -require-by 60s -format text
```

The shipped example intentionally exits nonzero for this illustrative deadline. Three of the four gap scenarios meet it. For a pull request, the composite action accepts `policy`, `trace`, `gap-flag`, and `require-by`. It writes the scenario table to the job summary even on a failed deadline. Omit the deadline in the CLI for an exploratory report, or omit `-format text` for JSON.

To try a different confirmation time without editing the live policy, run these commands from the repository root:

```sh
candidate=$(mktemp)
sed 's/for: 15s/for: 10s/' deploy/blackbox/guardrails.yaml > "$candidate"
go run ./cmd/flagguard -config "$candidate" -suite deploy/blackbox/cases/suite.json -format text
go run ./cmd/flagguard -config "$candidate" -replay deploy/blackbox/cases/sustained.json \
  -gap-check price_response_v2 -require-by 60s -format text
```

At 10 seconds, the baseline decision moves to 40 seconds. All three positions in its shorter confirmation interval meet the one-minute deadline, with the last decision at 55 seconds. Both settings pass the three original labeled cases. Shorter confirmation can react to shorter-lived faults. Evaluate that cost on incidents from your service before changing the deployed policy.

Times are measured from the **start of the trace**. Align the capture with the incident onset if your team measures its deadline from onset. A missing evaluation models the controller receiving no readings at one instant. All later rolling query values are preserved as recorded. Raw scrape loss, longer outages, flag writes, and client propagation require separate checks. A scenario that reaches the end without a decision supplies no timing evidence beyond that point.
