# Verification

All checkout traffic in these runs was synthetic. The controller verifies the settings in Flagsmith. The smoke test separately checks which checkout variant the application serves.

| Exercise | Recorded result |
| --- | --- |
| Checkout rollback | The landing-page replay recorded 39.6 seconds from injection to verified rollback. Both the default flag and experiment rollout override were disabled. All 20 follow-up requests succeeded on control. [Decision receipt](../internal/server/static/exhibit/checkout.json) |
| Live walkthrough | The filmed run recorded a 26.2% error rate on Variant B against a 5% limit, with zero observed errors on control. Recovery returned the controller to Healthy. [Watch the run](flagguard-demo-silent.mp4) |
| Shadow mode | A sustained breach produced a proposed rollback and left the flag enabled. [Result](evidence/shadow-rehearsal.json) |
| Deployment | The authenticated single-controller profile was exercised with mounted secrets, a read-only filesystem, persistent state, and a local HTTPS proxy. Public DNS and certificate renewal require a deployment host. [Result](evidence/production-smoke.json) |
| Runtime images | A scan of the two project images recorded zero known findings for those image digests at the time of the scan. [Digests and scanner version](evidence/image-scan-summary.json) |

A second developer also reported a successful run on a separate machine with an earlier build. The latency rule triggered there at 896.5 ms against a 500 ms limit. Rollback took 30 seconds, 20 follow-up requests succeeded on control, and recovery finished healthy. Detection time varies with traffic, scrape timing and Flagsmith API latency.

## Reproduce the checks

Start the local demo and run `python3 scripts/smoke.py`. After it finishes, run `python3 scripts/shadow-smoke.py` to exercise the same stack in shadow mode. The [full demo workflow](../.github/workflows/demo-smoke.yml) also tests the authenticated deployment. The [Go checks workflow](../.github/workflows/ci.yml) runs the tests, vet, dependency analysis, build and configuration checks.

Before enabling automatic changes for another service, test the team's own targeting rules, credentials, Prometheus queries and recovery procedure in staging. See the [operations guide](OPERATIONS.md).
