-- Deployment jobs gained an action (install | preflight | repair | upgrade | uninstall).
-- Existing rows predate multi-action jobs and were all installs.

ALTER TABLE deploy_jobs ADD COLUMN IF NOT EXISTS action TEXT NOT NULL DEFAULT 'install';
