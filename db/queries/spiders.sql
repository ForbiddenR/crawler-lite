-- name: ListSpiders :many
SELECT * FROM spiders
WHERE deleted_at IS NULL
ORDER BY name;

-- name: GetSpider :one
SELECT * FROM spiders
WHERE id = $1 AND deleted_at IS NULL;

-- name: CreateSpider :one
INSERT INTO spiders (project_id, name, description, entry_module, config, created_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: UpdateSpider :one
UPDATE spiders
SET name = $2, description = $3, entry_module = $4, config = $5,
    status = $6, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteSpider :exec
UPDATE spiders SET deleted_at = now() WHERE id = $1;
