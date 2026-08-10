-- stream_sessions (from 000001_initial.sql) was never read or written by any
-- Go code -- stream session tracking has always lived entirely in-memory in
-- stream.ReadAheadManager. Confirmed via a full-codebase grep before dropping.
drop table if exists stream_sessions;
