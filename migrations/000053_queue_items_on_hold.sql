-- Lets a user explicitly pause a specific queue item: PauseQueueItem cancels
-- any in-flight fetch for it and sets on_hold, ResumeQueueItem clears it.
-- Every automatic scan that could pick an item back up on its own
-- (ListPendingLibrarySearchTargets, ListFailedQueueRetryTargets,
-- ListUpgradableLibraryItems) excludes on_hold rows so a paused item stays
-- paused until the user explicitly resumes it.
alter table queue_items add column if not exists on_hold boolean not null default false;
