-- Seerr request reconciliation treats external_id + request_type as its
-- durable identity. Keep the oldest row from any historical race before
-- enforcing that invariant for atomic INSERT ... ON CONFLICT upserts.
delete from media_requests duplicate
using media_requests original
where duplicate.id > original.id
  and duplicate.external_id = original.external_id
  and duplicate.request_type = original.request_type;

create unique index media_requests_external_id_request_type_unique
    on media_requests (external_id, request_type);
