delete from library_items li
using episodes ep, tv_shows tv, queue_items qi
where li.episode_id = ep.id
  and ep.tv_show_id = tv.id
  and qi.library_item_id = li.id
  and li.available = false
  and qi.selected_release_id is null
  and qi.idempotency_key like '%-pack-%'
  and coalesce(tv.number_of_seasons, 0) > 0
  and ep.season_number > tv.number_of_seasons + 1;

delete from episodes ep
using tv_shows tv
where ep.tv_show_id = tv.id
  and coalesce(tv.number_of_seasons, 0) > 0
  and ep.season_number > tv.number_of_seasons + 1
  and not exists (
      select 1
      from library_items li
      where li.episode_id = ep.id
  );
