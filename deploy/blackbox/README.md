# Catch a broken price response that still returns HTTP 200

A client expects the coffee price in `amount_cents`. A release renames that field to `amount`. Both endpoints still return HTTP 200. The new response breaks clients that read the old field.

A Prometheus Blackbox Exporter probe requests each version and checks the decoded JSON body. FlagGuard watches each version's fraction of passing probes over 30 seconds. The candidate needs at least four observations. A pass fraction below 75% must persist for 15 seconds. A failed exporter scrape makes that arm's current readings unavailable. Control provides a comparison.

## Replay the failure

From the repository root, run:

```sh
make replay-price
```

This reads the included Prometheus capture and runs the FlagGuard decision loop locally. No running services or credentials are needed after the Go dependencies have been installed.

## Check a policy change

Run `make test-price-policy` from the repository root. Each case uses Prometheus measurements from the local two-endpoint fixture. The healthy capture starts after warmup. In the transient case we restored the correct field immediately after the first failed probe and checked that Prometheus recorded exactly one failure. In the sustained case the candidate served the renamed field continuously.

| What happened | Original 90% minimum | Current 75% minimum |
| --- | --- | --- |
| Healthy responses | Stayed enabled in replay | Stayed enabled in replay |
| Single malformed response | Would roll back | Stayed enabled in replay |
| Sustained schema change | Would roll back at 05:36:53 UTC | Would roll back at 05:36:58 UTC |

Both policies require 15 seconds below the limit. Each probe stays in the rolling average for 30 seconds. At six probes per window, one miss leaves five of six passing (83.3%). At the minimum volume of four probes, one miss leaves exactly 75%, and the comparison is strictly below. These results describe these captures. Choose thresholds for your own probe cadence and the cost of a missed response. [Inspect the three cases](cases/suite.json).

A separate [live enforce-mode run](../../docs/evidence/price-calibrated-rollback.json) checked that the 75% rule also preserved the enabled flag after one miss. A sustained break then disabled the flag in Flagsmith and the next client read returned the original price on control.

The earlier [single-incident trace](incident.json) ends at the original policy's decision. The sustained case in the suite includes enough later observations to evaluate the new limit.

## Run the probes against the fixtures

From this directory, start the isolated test stack. It uses Docker Compose and exposes Prometheus on loopback port 19090. The two fixed-price JSON endpoints and the exporter are internal to its Docker network.

```sh
docker compose up -d --force-recreate
```

In [the local Prometheus query console](http://localhost:19090/query), evaluate `probe_success{job="price_contract"}`. Wait for both variants to read 1 and for about a minute of baseline data. Then replace the candidate response:

```sh
docker compose exec -T prices cp /fixtures/renamed.json candidate.json
```

Query `probe_http_status_code{job="price_contract"}` and `probe_success{job="price_contract"}`. Both status codes should read 200. The candidate should score 0 on the contract and control should score 1. After about 35 seconds, evaluate the last minute of your measurements from the repository root:

```sh
PROMETHEUS_URL=http://localhost:19090 go run ./cmd/flagguard \
  -config deploy/blackbox/guardrails.yaml -backtest 1m \
  -expect price_response_v2=contract_pass_fraction -format text
```

Run `docker compose down` from this directory when finished. The fixture stack exercises probes and historical evaluation. To exercise live flag changes, connect a controller and a Flagsmith environment as described below.

## Protect your service

- In [the scrape job](prometheus.yml), set one stable probe target per variant for this policy. The targets must consistently serve their respective variants. Replace the exporter address with your internal exporter service.
- Edit [the probe](probes.yml) to check the body your client actually expects. This example checks a known SKU, currency and price for a fixed test item. Use a fixture or invariant appropriate to your service. [Exporter configuration](https://github.com/prometheus/blackbox_exporter/blob/v0.27.0/CONFIGURATION.md)
- Set the rollout flag in [the policy](guardrails.yaml) to the flag your application already evaluates. For a Flagsmith Experiment, also set its `experiment_id` so the controller checks its rollout override.
- Connect the policy using `GUARDRAILS_FILE` with the [standalone deployment](../../compose.standalone.yml). Set `PROMETHEUS_URL` to an address reachable from that controller. The policy defaults to shadow mode. Review a proposed rollback, including the control observations, before selecting enforce mode. [Deployment and recovery guide](../../docs/OPERATIONS.md)

### Reproduce the Flagsmith write in staging

The original 90% policy’s [recorded live decision](../../docs/evidence/price-contract-rollback.json) used a dedicated boolean flag. To repeat it, start with a fresh fixture stack and create an enabled `price_response_v2` flag in your disposable, self-hosted staging environment. Set `FLAGSMITH_API_URL`, `FLAGSMITH_ENV_KEY`, and `FLAGSMITH_ADMIN_TOKEN` in your host shell for that environment. Run the controller from the repository root on its own port and state file:

```sh
export PROMETHEUS_URL=http://127.0.0.1:19090
export FLAGGUARD_STATE_FILE="$(mktemp -d)/state.json"
go run ./cmd/flagguard -config deploy/blackbox/guardrails.yaml -check
go run ./cmd/flagguard -config deploy/blackbox/guardrails.yaml -mode enforce -listen 127.0.0.1:18086
```

Wait for a healthy baseline, then replace the candidate fixture as above. Open the [local incident history](http://127.0.0.1:18086/workspace) and inspect the price rollback. In Flagsmith, the staging flag should now be disabled. Use the following steps to verify the repair.

### Verify a fix while the flag is off

With the staging controller running in enforce mode, leave the malformed response in place after rollback. In the workspace, the price guardrail shows **Fix still failing**. Each cross is a direct probe of the candidate. Confirm that the flag remains disabled in Flagsmith.

1. Attempt a reset through the staging API. With the example loopback server and no operator token, use `curl -i -H 'Content-Type: application/json' -d '{}' http://127.0.0.1:18086/api/reset/price_response_v2`. An authenticated deployment also needs its operator credential. Expect HTTP 409. Select the held request in incident history to inspect which probes failed.
2. Restore the field on the candidate. From the repository root, run `docker compose -f deploy/blackbox/compose.yml exec -T prices cp /fixtures/price.json candidate.json`. Leave the flag disabled.
3. Wait for **Ready for review**. The policy requires a clean 20-second window with at least three direct probes and no gap over eight seconds. The public flag should still be off. A missing or stale series keeps recovery unavailable.
4. Select **Review recovery** and confirm. Inspect the reset in **Incident history** for the probe readings and the Flagsmith change. The controller then waits for the normal rollout metrics to warm up.

For a quick check without Docker or credentials, run `make rehearse-recovery` from the repository root. The test exercises the reset API using the two price fixtures and in-memory service responses.

In the [recorded self-hosted run](../../docs/evidence/price-recovery.json), the broken candidate produced HTTP 200 yet the reset returned 409. After we repaired the field, the next complete passing window was visible 23.4 seconds later. Five reads before recovery served control and five after confirmation served the repaired candidate. Each read resolved the Flagsmith flag and fetched the selected fixture. The timing depends on probe cadence and polling.
