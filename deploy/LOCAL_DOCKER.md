# crawler-lite Local Docker Instruction Manual

This guide is for running crawler-lite locally in Docker for testing.

No external reverse proxy is needed for local testing. The app is available directly at:

```text
http://127.0.0.1:8000
```

---

## 1. Prepare `.env`

From the repo root:

```sh
cp .env.prod.example .env
```

Edit `.env`.

For local testing, make sure the database settings match each other:

```env
POSTGRES_USER=crawler
POSTGRES_PASSWORD=crawler
POSTGRES_DB=crawler

DATABASE_DSN=postgres://crawler:crawler@postgres:5432/crawler?sslmode=disable
```

Also make sure these Docker-internal values are set:

```env
REDIS_ADDR=redis:6379
MINIO_ENDPOINT=minio:9000

HTTP_ADDR=:8000
GRPC_ADDR=:9000

MASTER_GRPC_ADDR=master:9000

WORKER_CRAWLERKIT_PATH=/opt/crawlerkit-py
WORKER_VENV_DIR=/var/lib/crawler-lite/venvs
UV_PATH=uv
```

For local testing, simple passwords are easier. If your password contains special characters, URL-encode it inside `DATABASE_DSN`.

---

## 2. Build Docker images

Normal build:

```sh
make prod-build VERSION=dev
```

This builds:

```text
crawler-lite/crawler-lite-master:dev
crawler-lite/crawler-lite-worker:dev
```

The master image uses an Alpine runtime.

---

## 3. Start the Docker stack

```sh
make prod-up
```

This starts:

- Postgres
- Redis
- MinIO
- master
- worker

Check status:

```sh
make prod-ps
```

Expected:

```text
crawler-lite-master-1     Up
crawler-lite-postgres-1   Up healthy
crawler-lite-redis-1      Up healthy
crawler-lite-minio-1      Up healthy
crawler-lite-worker-1     Up
```

`minio-init` may start and then exit. That is normal. It is a one-shot bucket initialization container.

---

## 4. Run database migrations

```sh
make prod-migrate
```

Expected final output:

```text
goose: successfully migrated database to version: 9
```

---

## 5. Verify the app

Health check:

```sh
curl http://127.0.0.1:8000/healthz
```

Expected:

```text
ok
```

Version check:

```sh
curl http://127.0.0.1:8000/api/version
```

Expected:

```json
{"version":"dev"}
```

Open the UI:

```text
http://127.0.0.1:8000
```

---

## 6. Create the first admin user

Generate a password hash:

```sh
docker compose -f docker-compose.yml -f docker-compose.prod.yml exec master \
  /master hash-password admin
```

Copy the generated hash. It will look like:

```text
$2a$10$...
```

Open Postgres:

```sh
docker compose -f docker-compose.yml -f docker-compose.prod.yml exec postgres \
  psql -U crawler -d crawler
```

Insert the admin user:

```sql
INSERT INTO users (email, password_hash, role, created_at, updated_at)
VALUES ('admin@local', '<PASTE_HASH_HERE>', 'admin', now(), now());
```

Exit `psql`:

```sql
\q
```

Now login at:

```text
http://127.0.0.1:8000
```

Credentials:

```text
Email: admin@local
Password: admin
```

---

## 7. View logs

All services:

```sh
make prod-logs
```

Only master:

```sh
docker compose -f docker-compose.yml -f docker-compose.prod.yml logs -f master
```

Only worker:

```sh
docker compose -f docker-compose.yml -f docker-compose.prod.yml logs -f worker
```

Only Postgres:

```sh
docker compose -f docker-compose.yml -f docker-compose.prod.yml logs -f postgres
```

---

## 8. Expose Postgres port to the host

By default, production-style Compose hides the Postgres port because `docker-compose.prod.yml` has:

```yaml
postgres:
  ports: !reset []
```

That means Postgres is reachable by other containers, but not from your host machine.

### Option A: edit `docker-compose.prod.yml`

Change the `postgres` service to:

```yaml
postgres:
  ports:
    - "127.0.0.1:5432:5432"
  restart: always
  volumes:
    - pg_data:/var/lib/postgresql/data
```

Then apply:

```sh
make prod-up
```

Now connect from the host:

```sh
psql "postgres://crawler:crawler@127.0.0.1:5432/crawler?sslmode=disable"
```

Or use a GUI database client:

```text
Host: 127.0.0.1
Port: 5432
User: crawler
Password: crawler
Database: crawler
```

Use your actual `.env` password.

### Option B: use a local override file

Create:

```yaml
# docker-compose.local-ports.yml
services:
  postgres:
    ports:
      - "127.0.0.1:5432:5432"
```

Start with the override:

```sh
docker compose \
  -f docker-compose.yml \
  -f docker-compose.prod.yml \
  -f docker-compose.local-ports.yml \
  up -d
```

This is cleaner if you only want Postgres exposed for local testing.

Prefer:

```yaml
127.0.0.1:5432:5432
```

Avoid this unless you intentionally want outside access:

```yaml
5432:5432
```

because it binds Postgres on all host interfaces.

---

## 9. Scale workers

Run more workers:

```sh
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d --scale worker=2
```

Check:

```sh
make prod-ps
```

---

## 10. Stop the stack

Stop containers but keep volumes/data:

```sh
make prod-down
```

Stop and delete local data volumes:

```sh
docker compose -f docker-compose.yml -f docker-compose.prod.yml down -v
```

Use `down -v` only when you want to reset local Postgres, Redis, MinIO, and worker venv data.

---

## 11. Common troubleshooting

### Master keeps restarting

Check logs:

```sh
docker compose -f docker-compose.yml -f docker-compose.prod.yml logs --tail=100 master
```

If you see:

```text
password authentication failed for user "crawler"
```

then your `.env` database password does not match the existing Postgres volume.

For local testing, fix `.env`:

```env
POSTGRES_USER=crawler
POSTGRES_PASSWORD=crawler
POSTGRES_DB=crawler
DATABASE_DSN=postgres://crawler:crawler@postgres:5432/crawler?sslmode=disable
```

Then reset the local data:

```sh
docker compose -f docker-compose.yml -f docker-compose.prod.yml down -v
make prod-up
make prod-migrate
```

### `/` returns 404

The master image probably does not contain the built frontend.

Rebuild:

```sh
make prod-build VERSION=dev
make prod-up
```

### Worker is running but spiders fail with crawlerkit path error

Inside Docker, the worker should use:

```env
WORKER_CRAWLERKIT_PATH=/opt/crawlerkit-py
```

Do not use your host path for Docker worker mode.

### Selenium works locally badly but works in Docker

That is expected. The Docker worker image includes Chromium and the required runtime libraries. For Selenium spiders, prefer the Docker worker.

---

## 12. Short local test checklist

Use this for a clean local Docker test:

```sh
cp .env.prod.example .env
# edit .env: set matching POSTGRES_PASSWORD and DATABASE_DSN

make prod-build VERSION=dev
make prod-up
make prod-migrate

curl http://127.0.0.1:8000/healthz
curl http://127.0.0.1:8000/api/version
```

Create admin:

```sh
docker compose -f docker-compose.yml -f docker-compose.prod.yml exec master \
  /master hash-password admin

docker compose -f docker-compose.yml -f docker-compose.prod.yml exec postgres \
  psql -U crawler -d crawler
```

Insert:

```sql
INSERT INTO users (email, password_hash, role, created_at, updated_at)
VALUES ('admin@local', '<PASTE_HASH_HERE>', 'admin', now(), now());
```

Open:

```text
http://127.0.0.1:8000
```

Login:

```text
admin@local
admin
```
