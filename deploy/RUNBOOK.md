# crawler-lite Operations Runbook

This runbook covers deploying, operating, backing up, and rolling back
crawler-lite in production. The deployment model is **Docker Compose on a
single VM** for the app stack: Postgres, Redis, MinIO, one master, and N
workers. TLS and reverse proxying are handled by the host/server
infrastructure outside this repository, using Nginx, Caddy, or your platform's
standard proxy. For multi-node/HA, see the non-goals in the slice plan — that's
a later piece of work.

## Architecture at a glance

```
       Host/server infrastructure (outside this repo)

 :80/:443 → Nginx/Caddy/etc.  TLS termination, reverse proxy
                 │
                 │ http://127.0.0.1:8000
            ┌────▼────┐
            │ Master  │  Go binary; SPA embedded via go:embed
            └─┬─────┬─┘
   :9000 gRPC  │     │ HTTP :8000
   (internal)  │     │ bound to 127.0.0.1 on the host
        ┌──────▼┐  ┌─▼────┐  ┌────────┐  ┌────────┐
        │Worker │  │Postgres│  │ Redis  │  │ MinIO  │
        │×N     │  └────────┘  └────────┘  └────────┘
        └───────┘
```

The master serves everything (API + WebSocket log stream + SPA). The production
Compose overlay publishes the master's HTTP port to `127.0.0.1:8000` so a
host-managed reverse proxy can reach it. gRPC between master and workers stays
inside the Compose network at `master:9000`.

## 1. First-time deploy

1. Configure the host/server reverse proxy outside this repository. Its upstream
   should be:

   ```text
   http://127.0.0.1:8000
   ```

   For WebSocket live task logs, make sure the proxy supports WebSocket upgrades.
   Caddy does this automatically; Nginx needs the usual `Upgrade` and
   `Connection` headers.

2. Deploy crawler-lite:

   ```sh
   git clone <repo> crawler-lite && cd crawler-lite
   cp .env.prod.example .env
   $EDITOR .env          # rotate EVERY CHANGE_ME secret
   make prod-build VERSION=$(git describe --tags --always --dirty)
   make prod-up
   make prod-migrate     # apply DB migrations (one-shot goose container)
   ```

Then verify the direct app port from the host:

```sh
docker compose -f docker-compose.yml -f docker-compose.prod.yml ps   # all healthy
curl -s http://127.0.0.1:8000/healthz        # → ok
curl -s http://127.0.0.1:8000/api/version    # → {"version":"…"}
```

Then verify through your external proxy, for example:

```sh
curl -s https://crawler.example.com/healthz
curl -s https://crawler.example.com/api/version
```

> **Module path note:** `go.mod` uses the placeholder
> `github.com/yourteam/crawler-lite`. The `IMAGE_REGISTRY` default
> (`crawler-lite`) builds locally-tagged images — fine for a single VM.
> Publishing to a real registry and renaming the module is a separate
> slice.

### External proxy examples

Minimal host-level Caddy example:

```caddy
crawler.example.com {
    reverse_proxy 127.0.0.1:8000
}
```

Minimal host-level Nginx location example:

```nginx
location / {
    proxy_pass http://127.0.0.1:8000;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
}
```

## 2. Seeding the admin user

There is no auto-seed. Create the first admin manually:

```sh
# 1. Generate a bcrypt hash for the admin password:
docker compose -f docker-compose.yml -f docker-compose.prod.yml exec master \
    /master hash-password 'your-admin-password'
# → $2a$10$...

# 2. Insert the user:
docker compose ... exec postgres psql -U crawler -d crawler -c \
    "INSERT INTO users (email, password_hash, role, created_at, updated_at)
     VALUES ('admin@example.com', '\$2a\$10\$...', 'admin', now(), now());"
```

After that, log in via your external proxy URL and rotate the password through
the profile flow if one exists.

## 3. Routine operations

| Action | Command |
|---|---|
| Tail logs (all) | `make prod-logs` |
| Status | `docker compose -f docker-compose.yml -f docker-compose.prod.yml ps` |
| Stop stack | `make prod-down` |
| Scale workers | `docker compose ... up -d --scale worker=4` |
| Shell into master | `docker compose ... exec master sh` |
| psql | `docker compose ... exec postgres psql -U crawler -d crawler` |

The master image uses an Alpine runtime, so `sh` and `apk` are available for
basic container debugging. The master process still runs as a non-root user.

## 4. Backup & restore

```sh
make backup                              # → ./backups/<timestamp>/
# Copy the whole directory off-host (S3, scp, rsync) for durability.
```

A backup contains `postgres.dump` (pg_dump custom format), `minio/`
(bucket mirror), and `manifest.txt`. Redis is **not** backed up — it's
ephemeral cache.

Restore (destructive — overwrites current state):

```sh
make prod-down                           # or at least: stop master
make restore BACKUP=./backups/<timestamp>
make prod-up
```

See `deploy/restore.sh` for the exact sequence. The restore drops and
recreates the DB to avoid `pg_restore` "already exists" errors.

**Cadence:** daily backups minimum; cron it. Keep at least 7 days
off-host. Test a restore into a throwaway stack quarterly.

## 5. Rollback

Rolling back to a previous image tag:

```sh
VERSION=v0.1.0 make prod-up
curl -s http://127.0.0.1:8000/api/version   # confirm direct app port
```

Compose pulls the specified tag and recreates the containers atomically
per service. **Database migrations are forward-only** (goose
convention). If the version you're rolling back to predates a migration
the current schema has applied, you must run `goose down` manually first
to revert that migration — otherwise the old binary may fail against the
newer schema. Check `db/migrations/` for what each version expects.

For a forward rollback (a bad release was deployed, revert to last
known-good): same as above with the last-known-good tag.

## 6. Known load characteristics

> **Fill this in after running `make load-test`.** The numbers below are
> placeholders — replace with measured results from your hardware.

Run `make load-test` against a stack with at least one worker. See
`loadtest/README.md` for interpretation.

| Metric | Value | Notes |
|---|---|---|
| API p95 latency (`/api/tasks` list) | _TBD_ | k6, 50 VUs |
| API error rate | _TBD_ | k6, should be < 1% |
| Tasks/min (queue burst, 1 worker) | _TBD_ | 50 quick spiders |
| Tasks/min (queue burst, 4 workers) | _TBD_ | scaled |

Record the master/worker resource limits from `.env` and the VM size so
the numbers are comparable across runs.

## 7. Troubleshooting

- **Workers don't connect:** check `WORKER_SHARED_SECRET` matches master
  and worker, and `MASTER_GRPC_ADDR=master:9000`. `docker compose logs
  worker` shows the gRPC handshake.
- **External proxy returns 502/503:** confirm the crawler-lite stack is up
  and the host proxy can reach `http://127.0.0.1:8000/healthz` from the
  same server.
- **Live task logs don't stream through the proxy:** confirm WebSocket
  upgrade headers are forwarded. Caddy handles this automatically; Nginx
  needs `proxy_http_version 1.1`, `Upgrade`, and `Connection` headers.
- **TLS/certificates fail:** this is owned by the host/server proxy, not
  the crawler-lite Compose stack. Check DNS, firewall, and your proxy's
  certificate automation.
- **`/` returns 404:** the master's embedded SPA wasn't built. Rebuild
  the image with `make prod-build` — the Dockerfile's frontend-build
  stage must succeed for `internal/web/dist/index.html` to exist.
- **Worker venv rebuilds every task:** the `worker_venvs` volume isn't
  mounted. Check `docker compose ... config | grep venvs` shows the
  volume on the worker service.
- **Screenshots blank in Selenium:** Chromium needs `--no-sandbox` when
  run as non-root in a container. The crawlerkit SDK should set this;
  if not, it's a SDK bug, not a deploy bug.
