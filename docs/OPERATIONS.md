# Operating FlagGuard

## Deployment contract

Supported target: one Linux container, one reviewed policy, and one dedicated Flagsmith environment, with durable state on a **local filesystem** and HTTPS in front of the HTTP listener. This is not multi-tenant SaaS or a highly available controller. Do not run replicas, mount state over NFS, or run multiple controllers with separate state files against the same flags. The local process lock prevents concurrent access to the same state file; it cannot coordinate separate volumes or hosts.

The bundled checkout stack is a development/demo environment. `FLAGGUARD_LOCAL_DEMO=true` explicitly allows its unauthenticated listener inside Docker; host ports remain loopback-only. Never expose that stack publicly. The production configuration contains no demo service or fault controls.

## Authentication and TLS

`compose.production.yml` requires an operator token, a Flagsmith management token, and an environment key as mounted files. The workspace, readiness endpoint, reports, and API require authentication. The landing page and basic liveness endpoint are public. Browsers use HTTP Basic: username **operator**, password **the generated operator token**. API clients can use `Authorization: Bearer <token>`. Generate at least 32 random characters; do not use a memorable password.

Use HTTPS for any remote access. The app does not terminate TLS itself. `deploy/Caddyfile.example` assumes Caddy runs on the same host and proxies to `127.0.0.1:8085`. Set the allowed hostname explicitly with `FLAGGUARD_DOMAIN`; unknown Host headers are rejected. Do not trust arbitrary forwarded authentication headers. Keep upstream Flagsmith/Prometheus connections private or use verified HTTPS URLs. No certificate verification bypass is implemented.

Authentication currently represents one operator role, not individual identities or RBAC. For a team, put an identity-aware proxy with MFA and individual access logs in front of the service as well. Avoid sharing the operator credential with untrusted viewers. Rotate it by replacing the file and recreating the container; Basic authentication may remain cached in a browser until its session is closed, but the old token is rejected by the restarted server.

## Configure an existing stack

1. Build/test the image from this repository; select an immutable image digest for deployment. Do not use the bundled database or demo stack as a production Flagsmith install.
2. Put secret files in a dedicated host directory with mode `0700`. Make each mounted file readable by container UID 10001 (for example mode `0444` inside that private directory, or ownership 10001 and mode `0400`). File contents never belong in Git. `_FILE` and a literal secret environment variable cannot both be set.
3. Copy `deploy/production.env.example` outside the repository and replace its **paths**, URLs, hostname, image digest, experiment ID, and `GUARDRAILS_FILE`. Use the actual experiment ID when protecting an experiment; `0` only covers a standalone environment flag. No placeholder credentials may be used. Flagsmith credentials need only the required environment permissions where supported.
4. Review the YAML. Start with `mode: shadow`; use query labels that uniquely select the intended service and variant. Set `recovery_warmup` at least as long as the longest query window plus a scrape/poll margin. Default is 35 seconds for the bundled 30-second window. Configure explicit sample gates. Control comparisons are diagnostic and do not suppress absolute treatment limits.
5. Run `docker compose --env-file /secure/flagguard.env -f compose.production.yml config --quiet`, then run the same command prefix with `run --rm --no-deps flagguard -check`. Preflight is read-only and checks the current treatment metrics; it does not prove mutation permissions or control readiness.
6. Start with the same prefix and `up -d`. Verify HTTPS, login, `/readyz`, and upstream connections. Run an authorized fault exercise against a staging service first. Switch to enforce mode only after inspecting shadow decisions and verifying the configured flag's behavior.

Docker Compose mounted secrets: https://docs.docker.com/compose/how-tos/use-secrets/
Caddy reverse proxy and automatic HTTPS: https://caddyserver.com/docs/quick-starts/reverse-proxy

### Standalone rehearsal

For a local rehearsal against existing services, create `data/standalone.env` in the repository and set the values below. Supply addresses reachable from inside the container. The example policy expects the checkout demo's metric names, so edit a copy for your application first.

```dotenv
FLAGSMITH_API_URL=http://your-flagsmith:8000/api/v1
FLAGSMITH_ADMIN_TOKEN=<management token>
FLAGSMITH_ENV_KEY=<environment key>
FLAGSMITH_EXPERIMENT_ID=<experiment ID>
PROMETHEUS_URL=http://your-prometheus:9090
FLAGGUARD_AUTH_TOKEN=<random operator password of at least 32 characters>
GUARDRAILS_FILE=./data/your-policy.yaml
```

Free local port 8085 before launching the standalone service. Run its preflight, then start it in shadow mode. The workspace login is `operator` with the operator token as its password.

```sh
docker compose --env-file data/standalone.env -f compose.standalone.yml build
docker compose --env-file data/standalone.env -f compose.standalone.yml run --rm --no-deps flagguard -check
docker compose --env-file data/standalone.env -f compose.standalone.yml run --rm --no-deps --service-ports flagguard -mode shadow
```

Each rule query must return one instant-vector series. For the example thresholds, error rate is a fraction and latency is in milliseconds. Use matching windows and units for the control queries. Missing treatment samples clear pending breach timers. Missing control data affects the comparison while the absolute treatment rules stay active. To protect a plain flag, omit the experiment ID from the policy or use zero.

## Monitor the controller

- `/healthz` is liveness only: the HTTP process is running. Docker uses this endpoint; it does not imply safe rollouts.
- Authenticated `/readyz` returns 503 for missing/stale evaluations, dependency errors, unavailable metrics, action-required state, or persistence errors. A rolled-back rollout can be ready for operator review. This is not a business KPI.
- `/api/status` includes `storage_ok`. If false, inspect disk capacity and permissions immediately. The UI surfaces the problem. A rollback/recovery intent must be persisted before any corresponding API write.
- Structured controller events go to stdout. Container log rotation is configured. The JSON report retains only the latest 200 events; export important incidents to your own storage. This is not an immutable compliance ledger.
- Alert on unreachable/unready controller, ACTION_REQUIRED, and storage errors. Webhooks are best-effort, not a durable delivery queue. Missing a notification does not undo a rollback.

## Incident and recovery procedure

For ACTION_REQUIRED, inspect both the default flag and experiment rollout in Flagsmith. A multi-resource change may have partially succeeded. The controller deliberately does not retry ambiguous writes automatically. Confirm the application's served variant independently; configuration read-back is not proof of propagation.

Fix the application and turn off any test fault. Review the retained evidence, then use **Review recovery**. The controller persists recovery intent before enabling either resource and retains the configured warm-up deadline across restarts. If storage fails after the API call, the controller stays locked for review. Never delete state to bypass a lock.

Flagsmith's rollout update currently includes its current value/weights because the API requires them. Concurrent edits by another writer are not transactionally coordinated. Assign one owner to the affected flags during automated enforcement. Arbitrary identity/segment overrides are outside the tested rollback scope; verify your flag model before enabling this controller.

## Backups, upgrades, and restore

Stop the controller before copying its state volume. Keep the full `state.json`, policy, image digest, and configuration metadata together; store credentials separately. The adjacent `.lock` file may remain: its inode is intentionally persistent and the OS releases its lock on process exit. Do not delete that file while any controller may be running.

For upgrades, stop the old container, back up state, start the new image in staging with a copied policy, and perform the scenario suite. Preserve the production volume when replacing the image. On restore, compare the saved rollback locks against actual Flagsmith settings before re-enabling anything; an old backup may not contain the latest incident. File writes and directory renames are synced, but this does not replace host/storage backups or hardware durability guarantees.

## Release acceptance still required

A production candidate must pass automated tests, dependency/image scans, authenticated staging deployment, TLS verification, a restart/failure exercise, and the intended team's policy review. A passing local demo is not approval for arbitrary production traffic. See VERIFICATION.md for checks actually performed and any pending release gates.
