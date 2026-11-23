# Verification

All checkout traffic in these runs was synthetic. The controller verifies the settings in Flagsmith. The smoke test separately checks which checkout variant the application serves.

| Exercise | Recorded result |
| --- | --- |
| PostgreSQL catalog lookup | Both SQL versions returned the same row. Prometheus 3.5.0 scraped 976 candidate buffer accesses and 3 for control on PostgreSQL 14.19. The shadow replay triggered at poll 3. With an expression index, both measured 3 and four polls passed. Renaming the table made the exporter return HTTP 503. All four later polls lacked measurements and the release check exited nonzero. [Replay the captured cases](../deploy/postgres/cases/) |
| Price recovery while disabled | Both endpoints returned HTTP 200. A premature recovery request returned HTTP 409 and five reads got the correct price on control. After repairing the candidate, we observed readiness after 23.4 seconds. Four probes passed in the 20-second window. An operator confirmed from the browser and five further reads got the correct price on the candidate. The failing and ready states were checked at 390 px and 1400 px. [Recovery receipt](evidence/price-recovery.json) |
| Checkout rollback | The landing-page replay recorded 39.6 seconds from injection to verified rollback. Both the default flag and experiment rollout override were disabled. All 20 follow-up requests succeeded on control. [Decision receipt](../internal/server/static/exhibit/checkout.json) |
| Live walkthrough | The filmed run recorded a 26.2% error rate on Variant B against a 5% limit, with zero observed errors on control. Recovery returned the controller to Healthy. |
| Historical policy replay | On the filmed two-minute trace, the shipped policy selects the error rule at 17:48:37 UTC. A 30% error limit selects the latency rule. Raising both limits leaves all 25 polls eligible with no trigger in the window. [Reproduce all three outcomes](POLICY_REPLAY.md) |
| Aligned live measurements | A fresh synthetic fault took 36.3 seconds to verified rollback. P99 triggered, all 20 follow-up requests succeeded on control, and manual recovery ended Healthy. A subsequent historical query at the decision timestamp reproduced all six readings: treatment and control request volumes, error rates, and P99 latency. |
| Price API compatibility | Both variant endpoints returned HTTP 200. Two of six candidate probes and all six control probes passed at decision time. The controller disabled the dedicated flag, verified the change, and a verification client resolved control and read the original price. [Decision receipt](evidence/price-contract-rollback.json) and [replay instructions](../deploy/blackbox/README.md) |
| Price policy calibration | On the same three recorded scenarios the 90% rule proposed a rollback after a single bad probe. The 75% rule passed all three and detected the sustained break five seconds later. A separate live run left the flag enabled after one miss, disabled it for the persistent break, and served the expected price on control. [Live receipt](evidence/price-calibrated-rollback.json) and [replay cases](../deploy/blackbox/cases/suite.json) |
| Shadow mode | A sustained breach produced a proposed rollback and left the flag enabled. [Result](evidence/shadow-rehearsal.json) |
| Deployment | The authenticated single-controller profile was exercised with mounted secrets, a read-only filesystem, persistent state, and a local HTTPS proxy. Public DNS and certificate renewal require a deployment host. [Result](evidence/production-smoke.json) |
| Runtime images | A scan of the two project images recorded zero known findings for those image digests at the time of the scan. [Digests and scanner version](evidence/image-scan-summary.json) |

An earlier build also completed the smoke test on a separate machine. The latency rule triggered there at 896.5 ms against a 500 ms limit. Rollback took 30 seconds, 20 follow-up requests succeeded on control, and recovery finished healthy. Detection time varies with traffic, scrape timing and Flagsmith API latency.

## Clean Ubuntu laptop trial

The project author ran the checkout lab on a second laptop with Ubuntu 26.04.1. In the initial run, the live error rule triggered at 20:06:50 UTC. The smoke test reported a 33-second rollback and 20 successful checkout requests on control. Replaying Prometheus history on a fixed five-second grid instead selected latency at 20:06:45 UTC. The exporter returned HTTP 409 because those decisions differed.

After the poll-recording fix, the author exported a fresh incident and ran its suite offline on the laptop. The recorded policy reproduced the error-rate decision on poll 6 and passed. Changing only the error confirmation from 15 seconds to one minute made the latency rule trigger on poll 6 of the same trace. The proposed policy still caught the incident, and the suite flagged the change in trigger. Both replays had sufficient data on all six evaluated polls. Run `make replay-ubuntu` to check the original trigger and the protection deadline against the exported files. [Original decision](evidence/ubuntu-checkout/decision.json) · [Six recorded polls](evidence/ubuntu-checkout/trace.json). [How incident export works](POLICY_REPLAY.md#turn-a-retained-rollback-into-a-test)

## Reproduce the checks

Start the local demo and run `python3 scripts/smoke.py`. After it finishes, run `python3 scripts/shadow-smoke.py` to exercise the same stack in shadow mode. The [full demo workflow](../.github/workflows/demo-smoke.yml) also tests the authenticated deployment. The [Go checks workflow](../.github/workflows/ci.yml) runs the tests, vet, dependency analysis, build and configuration checks.

Before enabling automatic changes for another service, test the team's own targeting rules, credentials, Prometheus queries and recovery procedure in staging. See the [operations guide](OPERATIONS.md).
