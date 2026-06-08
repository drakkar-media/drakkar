-- Change vote_count from int4 to int8 so TMDB vote counts (or Seerr IDs)
-- that exceed int32 max (2,147,483,647) don't overflow.
ALTER TABLE movies   ALTER COLUMN vote_count TYPE bigint;
ALTER TABLE tv_shows ALTER COLUMN vote_count TYPE bigint;
