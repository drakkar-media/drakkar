with show_counts as (
    select tv.id as tv_show_id,
           coalesce(tv.number_of_episodes, 0) as number_of_episodes,
           count(distinct li.id) filter (
               where li.available
                 and ep.season_number > 0
                 and ep.episode_number > 0
           ) as available_episodes
    from tv_shows tv
    join episodes ep on ep.tv_show_id = tv.id
    join library_items li on li.episode_id = ep.id
    group by tv.id, tv.number_of_episodes
),
ranked_items as (
    select li.id as library_item_id,
           li.available,
           qi.state,
           qi.selected_release_id,
           row_number() over (
               partition by tv.id
               order by ep.season_number, ep.episode_number, li.id
           ) as episode_ordinal,
           sc.number_of_episodes
    from tv_shows tv
    join show_counts sc on sc.tv_show_id = tv.id
    join episodes ep on ep.tv_show_id = tv.id
    join library_items li on li.episode_id = ep.id
    left join queue_items qi on qi.library_item_id = li.id
    where sc.number_of_episodes > 0
      and sc.available_episodes >= sc.number_of_episodes
      and ep.season_number > 0
      and ep.episode_number > 0
),
target_items as (
    select library_item_id
    from ranked_items ri
    where episode_ordinal > number_of_episodes
      and available = false
      and state = 'failed'
      and selected_release_id is null
      and not exists (
          select 1
          from symlink_publications sp
          where sp.library_item_id = ri.library_item_id
      )
)
delete from library_items li
using target_items ti
where li.id = ti.library_item_id;

with show_counts as (
    select tv.id as tv_show_id,
           coalesce(tv.number_of_episodes, 0) as number_of_episodes,
           count(distinct li.id) filter (
               where li.available
                 and ep.season_number > 0
                 and ep.episode_number > 0
           ) as available_episodes
    from tv_shows tv
    join episodes ep on ep.tv_show_id = tv.id
    join library_items li on li.episode_id = ep.id
    group by tv.id, tv.number_of_episodes
),
ranked_episodes as (
    select ep.id as episode_id,
           row_number() over (
               partition by tv.id
               order by ep.season_number, ep.episode_number, ep.id
           ) as episode_ordinal,
           sc.number_of_episodes
    from tv_shows tv
    join show_counts sc on sc.tv_show_id = tv.id
    join episodes ep on ep.tv_show_id = tv.id
    where sc.number_of_episodes > 0
      and sc.available_episodes >= sc.number_of_episodes
      and ep.season_number > 0
      and ep.episode_number > 0
),
target_episodes as (
    select episode_id
    from ranked_episodes re
    where episode_ordinal > number_of_episodes
      and not exists (
          select 1
          from library_items li
          where li.episode_id = re.episode_id
      )
)
delete from episodes ep
using target_episodes te
where ep.id = te.episode_id;
