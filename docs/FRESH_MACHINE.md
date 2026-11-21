# Run the checkout lab locally

Use Docker with Linux containers, Compose, Python 3, and free ports 8000, 8081, 8085 and 9090. From a clone of this repository, run `make demo`. Docker builds the Go binaries. The initial image downloads and database migrations can take several minutes.

## Without Make

Run these commands from the repository root. With the standalone Compose executable, substitute `docker-compose` for `docker compose`.

```sh
python3 scripts/init-env.py
docker compose up -d flagsmith flagsmith-task-processor prometheus
python3 scripts/bootstrap.py
docker compose up --build -d
```

The local account details are in `data/local-login.txt`. The generated `.env` holds the demo credentials. Keep both on your machine.

## Check the installation

Open [the workspace](http://localhost:8085/workspace) and wait for Healthy. The checkout service needs to generate enough traffic for the first measurement window.

```sh
docker compose exec -T flagguard /app/service -check
python3 scripts/smoke.py
```

The first command checks the connections and measurements. The second runs a complete checkout incident: it injects a fault, verifies both Flagsmith settings, sends 20 requests to check that control is serving traffic, and restores the rollout. Inspect `tmp/smoke-result.json` for the outcome.

## If startup is slow

Use `docker compose ps -a` and `docker compose logs --tail=50 flagsmith` to check whether migrations are progressing. On an emulated Docker VM, `make local-demo` uses the host Go toolchain to cross-compile for Docker's architecture. If synthetic traffic saturates the VM, set `DEMO_RPS=3` in `.env`, then run `docker compose up -d --no-build --no-deps demo-app`. Wait for fresh measurements before starting another incident.

To stop the stack while keeping its database and controller state, use `make down`. To start the configured stack again, use `make up`.
