#!/bin/sh
# Back up crawler-lite state: Postgres (custom-format dump) + MinIO bucket
# (mirrored out via a one-shot mc container). Redis is intentionally
# skipped — it holds only ephemeral cache/pubsub state.
#
# Usage:
#   ./deploy/backup.sh
#   BACKUP_DIR=/mnt/backups ./deploy/backup.sh
#
# Output: a timestamped directory containing:
#   postgres.dump   — pg_dump -Fc output (restorable via pg_restore)
#   minio/          — mirrored contents of MINIO_BUCKET
#   manifest.txt    — taken_at + image versions
#
# Requires: docker, docker compose, and the same .env the stack runs with
# (for POSTGRES_* / MINIO_* credentials).

set -eu

# Load .env if present so we don't require the caller to export vars.
if [ -f .env ]; then
    set -a
    # shellcheck disable=SC1091
    . ./.env
    set +a
fi

: "${POSTGRES_USER:=crawler}"
: "${POSTGRES_DB:=crawler}"
: "${MINIO_ROOT_USER:=crawler}"
: "${MINIO_ROOT_PASSWORD:=crawler-secret}"
: "${MINIO_BUCKET:=crawler-artifacts}"
: "${BACKUP_DIR:=./backups}"

STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
OUT="${BACKUP_DIR}/${STAMP}"
mkdir -p "${OUT}/minio"

echo "==> Backing up Postgres → ${OUT}/postgres.dump"
docker compose -f docker-compose.yml -f docker-compose.prod.yml exec -T postgres \
    pg_dump -U "${POSTGRES_USER}" -Fc -d "${POSTGRES_DB}" \
    > "${OUT}/postgres.dump"

echo "==> Backing up MinIO bucket '${MINIO_BUCKET}' → ${OUT}/minio/"
# Run mc in a throwaway container on the same network as the stack so it
# can resolve `minio`. The compose project name defaults to the directory
# name (crawler-lite).
PROJECT="${COMPOSE_PROJECT_NAME:-crawler-lite}"
docker run --rm \
    --network "${PROJECT}_default" \
    -v "$(pwd)/${OUT}/minio:/data" \
    --entrypoint sh \
    minio/mc:latest -c \
    "mc alias set src http://minio:9000 \"${MINIO_ROOT_USER}\" \"${MINIO_ROOT_PASSWORD}\" &&
     mc mirror --overwrite src/${MINIO_BUCKET} /data"

echo "==> Writing manifest"
{
    echo "taken_at: ${STAMP}"
    echo "postgres_user: ${POSTGRES_USER}"
    echo "postgres_db: ${POSTGRES_DB}"
    echo "minio_bucket: ${MINIO_BUCKET}"
    echo "image_master: $(docker compose -f docker-compose.yml -f docker-compose.prod.yml images -q master 2>/dev/null || echo unknown)"
    echo "image_worker: $(docker compose -f docker-compose.yml -f docker-compose.prod.yml images -q worker 2>/dev/null || echo unknown)"
} > "${OUT}/manifest.txt"

echo "==> Backup complete: ${OUT}"
