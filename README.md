# FlagGuard

FlagGuard switches off a failing experiment in self-hosted Flagsmith. It watches error rate and latency in Prometheus, checks that the problem lasts, and keeps a record of what it changed.

[Watch the 2:36 walkthrough](docs/flagguard-demo-silent.mp4) · [Follow the demo](docs/JUDGE_GUIDE.md) · [Connect your own service](docs/OPERATIONS.md#configure-an-existing-stack)

![The FlagGuard landing page, with a track switch representing the two checkout routes](docs/images/landing.jpg)

## Try it on your machine

You'll need Docker with Compose, Python 3, Make, and free ports `8000`, `8081`, `8085`, and `9090`. The first run downloads images and runs Flagsmith's database migrations.

```sh
git clone https://github.com/Rishabh1925/FlagGuard.git
cd FlagGuard
make demo
```

Open [FlagGuard on localhost](http://localhost:8085) and select **Open workspace**. Setup creates a local Flagsmith account and splits synthetic checkout traffic between the original version (control) and a new version (Variant B). You can inspect the experiment in [Flagsmith](http://localhost:8000) using the generated login in `data/local-login.txt`.

1. Wait for **Healthy**, then select **Inject fault into Variant B**. The lab adds delay and failed requests to the experimental checkout.
2. Watch the two graphs. The demo requires at least 20 estimated requests in a 30-second window. An error rate above 5% or P99 latency above 500 ms must persist for at least 15 seconds.
3. When the status changes to **Rolled back**, open **Incident history** and select the rollback. You'll see which rule fired, what control was doing, and which Flagsmith settings changed.
4. Select **Review recovery** when you're ready to try again. Recovery stops the fault, re-enables the rollout, and waits for fresh measurements.

![The live workspace showing a 14.7% error rate and 891.5 ms P99 latency on Variant B during breach confirmation](docs/images/checkout-incident.jpg)

*During this run, Variant B was already failing while the original checkout had zero observed errors. The countdown shows why FlagGuard was still waiting.*

## Why it checks two settings

A running Flagsmith experiment can override its environment flag. Turning off the default alone can leave some people on the broken checkout. When a policy names an experiment, FlagGuard disables both the default flag and the experiment rollout override, then reads both settings back through the management API. An uncertain write leaves the rollout locked for operator review.

There's a separate question: what did the application actually serve? In the incident replay on the landing page, the smoke test measured **39.6 seconds** from fault injection to verified rollback. It then sent **20 fresh checkout requests**. All 20 succeeded on control. You can inspect the [original measurements](internal/server/static/exhibit/checkout.json) or run the same test against the local demo:

```sh
python3 scripts/smoke.py
```

This test injects its own fault and recovers the demo at the end. Use `make down` to stop the stack and keep its data. For first-boot issues, slower Docker VMs, or setup without Make, see the [local setup guide](docs/FRESH_MACHINE.md).

## Use it with an existing application

The controller is a Go binary with an embedded web interface. Your application serves the flag variants and exposes metrics. FlagGuard queries Prometheus and uses the self-hosted Flagsmith API to change rollout settings. The checkout app is only needed for the demo.

| What you want to do | Where to start |
| --- | --- |
| Define a guardrail | Edit the flag, experiment ID, PromQL queries, thresholds, and confirmation times in [guardrails.yaml](guardrails.yaml). |
| Check the connections | Run the controller with `-check` for a read-only check of the flag, sample volume, and treatment queries. |
| Rehearse a policy | Set `mode: shadow` to record proposed rollbacks while the rollout stays enabled. |
| Deploy the controller | Start with the [standalone Compose file](compose.standalone.yml). The [operations guide](docs/OPERATIONS.md) covers mounted secrets, HTTPS, durable state, and manual recovery for a single controller. |

## Development

```sh
make test                         # Go tests with the race detector
go vet ./...                      # Static checks
node scripts/dashboard.test.cjs    # Dashboard behaviour
make build                        # Build both Go binaries in bin/
```

The controller entry point is [cmd/flagguard](cmd/flagguard). The decision loop lives in [internal/engine](internal/engine), and the Flagsmith integration lives in [internal/flagsmith](internal/flagsmith). See the [verification record](docs/VERIFICATION.md) for the checks behind the demo.

Released under the [MIT license](LICENSE).
