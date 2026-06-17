-- name: ListTasks :many
SELECT * FROM tasks
ORDER BY queued_at DESC
LIMIT $1 OFFSET $2;

-- name: GetTask :one
SELECT * FROM tasks WHERE id = $1;

-- name: CreateTask :one
INSERT INTO tasks (spider_id, trigger, spider_version, triggered_args, created_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: SetTaskStatus :one
UPDATE tasks
SET status = $2,
    error = COALESCE($3, error),
    error_class = COALESCE($4, error_class),
    started_at = CASE WHEN $2 = 'running' AND started_at IS NULL THEN now() ELSE started_at END,
    finished_at = CASE WHEN $2 IN ('succeeded','failed','cancelled','timeout','captcha_blocked')
                       THEN now() ELSE finished_at END
WHERE id = $1
RETURNING *;
