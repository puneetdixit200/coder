-- name: InsertWorkspaceBuildOrchestration :one
INSERT INTO workspace_build_orchestrations (
    id,
    created_at,
    updated_at,
    parent_build_id,
    child_transition,
    child_template_version_id,
    child_template_version_preset_id,
    child_rich_parameter_values,
    child_log_level,
    child_reason,
    status,
    error
)
VALUES (
    @id,
    @created_at,
    @updated_at,
    @parent_build_id,
    @child_transition,
    @child_template_version_id,
    @child_template_version_preset_id,
    @child_rich_parameter_values,
    @child_log_level,
    @child_reason,
    'pending',
    NULL
)
RETURNING *;

-- name: GetWorkspaceBuildOrchestrationByID :one
SELECT
    *
FROM
    workspace_build_orchestrations
WHERE
    id = @id;

-- name: GetWorkspaceBuildOrchestrationByParentBuildID :one
SELECT
    *
FROM
    workspace_build_orchestrations
WHERE
    parent_build_id = @parent_build_id;

-- name: GetWorkspaceBuildOrchestrationByChildBuildID :one
SELECT
    *
FROM
    workspace_build_orchestrations
WHERE
    child_build_id = @child_build_id;

-- name: GetNextPendingWorkspaceBuildOrchestrationForUpdate :one
-- Must be called from within a transaction. The row lock is released
-- when the transaction ends.
SELECT
    wbo.*
FROM
    workspace_build_orchestrations wbo
    JOIN workspace_builds wb ON wbo.parent_build_id = wb.id
    JOIN provisioner_jobs pj ON wb.job_id = pj.id
WHERE
    wbo.status = 'pending'
    -- Include all terminal parent states so pending orchestration
    -- rows are processed and resolved even when no child build should
    -- be created.
    AND pj.job_status IN ('succeeded', 'failed', 'canceled')
ORDER BY
    wbo.created_at ASC
LIMIT 1
FOR UPDATE OF wbo SKIP LOCKED;

-- name: UpdateWorkspaceBuildOrchestrationCompletedByID :one
UPDATE
    workspace_build_orchestrations
SET
    child_build_id = @child_build_id,
    status = 'completed',
    error = NULL,
    updated_at = @updated_at
WHERE
    id = @id
    AND status = 'pending'
RETURNING *;

-- name: UpdateWorkspaceBuildOrchestrationFailedByID :one
UPDATE
    workspace_build_orchestrations
SET
    status = 'failed',
    error = @error,
    updated_at = @updated_at
WHERE
    id = @id
    AND status = 'pending'
RETURNING *;

-- name: UpdateWorkspaceBuildOrchestrationCanceledByID :one
UPDATE
    workspace_build_orchestrations
SET
    status = 'canceled',
    error = NULL,
    updated_at = @updated_at
WHERE
    id = @id
    AND status = 'pending'
RETURNING *;
