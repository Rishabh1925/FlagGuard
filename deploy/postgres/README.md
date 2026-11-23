# Guard a PostgreSQL lookup

A catalog change can return the right item and still scan the table on every lookup. This integration gives FlagGuard a buffer budget for a query behind a Flagsmith flag. It needs no application instrumentation. The probe runs the two SQL files you supply against a staging database and exports one gauge per variant to Prometheus.

## Replay the captured trial

From the repository root, run `make test-postgres-policy`. The saved cases run offline. One exercises a candidate that lost its index. The other repeats the same query after adding an index for its expression. The suite checks that the first case triggers the policy and the repaired case stays online in replay.

The fixture has 40,000 synthetic catalog items. Both queries return the title for ID 4242. The candidate wraps the indexed column in `id + 0`. PostgreSQL 14.19 used 3 shared-buffer accesses for the control and 976 for the candidate in the captured regression. The experiment measures work per execution. A hit means PostgreSQL found a block in its shared buffer cache. Adding hits and reads counts repeated accesses too. [PostgreSQL EXPLAIN reference](https://www.postgresql.org/docs/14/sql-explain.html).

## Try it against a database

Use a disposable database for the example. The fixture creates one table and the repair adds an index. The commands assume PostgreSQL client tools are on your path and your database role can create a database. From the repository root:

```sh
createdb flagguard_catalog_trial
export PGDATABASE=flagguard_catalog_trial
psql -X -v ON_ERROR_STOP=1 -f deploy/postgres/fixture.sql
python3 deploy/postgres/probe.py --control deploy/postgres/control.sql --candidate deploy/postgres/candidate.sql
```

Open `http://127.0.0.1:9328/metrics` to see both readings. The probe uses one trusted, simple SELECT per file. Each scrape executes both using EXPLAIN ANALYZE, a read-only transaction and a two-second statement timeout. Use a read-only role on staging when supplying your own queries. A failed measurement makes the scrape return HTTP 503. SQL text, results and connection details stay out of the response.

Start Prometheus with [the scrape configuration](prometheus.yml), or copy its job into an existing configuration. The example assumes Prometheus and the probe share the same host network. For a containerized scraper, bind the probe to an address the scraper can reach and update its target. The example scrapes once a second to make the small fixture easy to try. For your own service, choose a cadence and confirmation time that fit the staging query cost.

After Prometheus shows the target as up and has accumulated four seconds of readings, run the release check from the repository root:

```sh
go build -o bin/flagguard ./cmd/flagguard
bin/flagguard -config deploy/postgres/guardrails.yaml -backtest 3s -expect catalog_lookup_v2=none -format text
```

The scan should fail the release check. The output includes the candidate reading, budget, and control reading. Run the following command with the same database selected, wait for four fresh seconds of healthy scrapes, and repeat the check. It should now pass.

```sh
psql -X -v ON_ERROR_STOP=1 -f deploy/postgres/repair.sql
```

Add `-save-trace catalog-trial.json` to a backtest to keep its inputs for CI. When the database or probe is unavailable, the release check requires complete treatment data and exits nonzero.

## Use it with your rollout

Point the probe at the two queries your application selects with its flag. Set the flag name in [guardrails.yaml](guardrails.yaml), replace the 100-block example budget with one measured on representative data, and set the experiment ID if the flag has an active experiment rollout. The control query helps explain a breach. The candidate's own budget determines the rollback. Configure your self-hosted Flagsmith and Prometheus connections, run the controller's read-only check, then observe it in shadow mode. [Connect an existing stack](../../docs/OPERATIONS.md#configure-an-existing-stack).
