#!/bin/sh
# Restore crawler-lite state from a backup directory produced by backup.sh.
#
# Usage:
#   ./deploy/restore.sh ./backups/20260101T120000Z
#   BACKUP=./backups/20260101T120000Z ./deploy/restore.sh
#
# WARNING: this OVERWRITES the current Postgres database and MinIO bucket
# contents. Stop the master first (it holds open DB connections and will
# fight the restore):
#
#   docker compose -f docker-compose.yml -f docker-compose.prod.yml stop master
#
# Then run this script, then restart the master.

set -eu

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

BACKUP="${1:-${BACKUP:?usage: restore.sh <backup-dir>}}"
if [ ! -f "${BACKUP}/postgres.dump" ]; then
    echo "error: ${BACKUP}/postgres.dump not found" >&2
    exit 1
fi

COMPOSE="docker compose -f docker-compose.yml -f docker-compose.prod.yml"

echo "==> Restoring Postgres from ${BACKUP}/postgres.dump"
# Drop+recreate to avoid "already exists" collisions from pg_restore.
# The data volume persists, so the DB must be cleared in place.
${COMPOSE} exec -T postgres psql -U "${POSTGRES_USER}" -d postgres -c \
    "DROP DATABASE IF EXISTS \"${POSTGRES_DB}\" WITH (FORCE);"
${COMPOSE} exec -T postgres psql -U "${POSTGRES_USER}" -d postgres -c \
    "CREATE DATABASE \"${POSTGRES_DB}\";"
${COMPOSE} exec -T postgres pg_restore -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" \
    --no-owner --no-privileges < "${BACKUP}/postgres.dump"

echo "==> Restoring MinIO bucket '${MINIO_BUCKET}' from ${BACKUP}/minio/"
if [ -d "${BACKUP}/minio" ]; then
    PROJECT="${COMPOSE_PROJECT_NAME:-crawler-lite}"
    # Ensure the bucket exists (minio-init may not have run in a restore
    # scenario), then mirror the backup back in.
    docker run --rm \
        --network "${PROJECT}_default" \
        -v "$(pwd)/${BACKUP}/minio:/data" \
        --entrypoint sh \
        minio/mc:latest -c \
        "mc alias set dst http://minio:9000 \"${MINIO_ROOT_USER}\" \"${MINIO_ROOT_PASSWORD}\" &&
         (mc mb -p dst/${MINIO_BUCKET} || true) &&
         mc mirror --overwrite /data dst/${MINIO_BUCKET}"
else
    echo "    (no minio/ dir in backup — skipping object restore)"
fi

echo "==> Restore complete. Restart the master:"
echo "    ${COMPOSE} up -d master"
