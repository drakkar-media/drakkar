-- v0.3.39 could dispatch a new Seerr item immediately before asynchronous
-- enrichment persisted its title. Requeue only that exact transient failure
-- once a usable title exists; unrelated failed searches remain untouched.
update queue_items q
set state = 'requested',
    failure_reason = '',
    last_searched_at = null,
    updated_at = now()
from library_items li
where li.id = q.library_item_id
  and q.state = 'failed'
  and q.failure_reason = 'no title available to verify search results against'
  and btrim(coalesce(li.title, '')) <> '';
