# Archive Support

Stored RAR `m0` support is still incomplete, but the ingest path now detects imported RAR volume sets and persists archive plus volume metadata for later parsing.

Implemented:

- detect `.partNN.rar`, `.rar`, `.r00`-style imported volume groups during NZB ingest
- persist `archives` and `archive_volumes` rows linked to `selected_releases`
- fetch a small prefix from the first RAR volume when NNTP access is available
- parse basic RAR4 headers
- persist `archive_entries`
- persist archive entry source metadata such as packed size, source volume index and archive payload offset
- persist `archive_ranges` for entries whose payload offsets are known from inspected headers
- create `stored_rar` virtual files for supported playable entries by mapping archive payload ranges into `virtual_file_ranges`
- open `stored_rar` content through a dedicated stream-layer reader type
- enforce contiguous stored-RAR span coverage in the reader before serving bytes
- fail publication when archive inspection yields no publishable virtual media files, allowing next-candidate fallback
- expose archive entry details in the release API/UI for operator inspection
- reject parsed playable entries whose mapped archive ranges do not fully cover the stored payload
- after selected-release import/indexing, if archive inspection produced a durable archive reject reason and zero publishable virtual files, workflow now promotes the next candidate before publication runs
- when publish still fails for missing virtual files, use archive rejection detail as the fallback failure reason when available
- treat `archive_*` failure reasons as hard candidate rejections so incompatible releases do not loop through retry paths
- add hard-rejected archive release URLs to the persistent blocklist and mark matching future search candidates rejected before selection
- store explicit reject reasons such as `archive_compression_unsupported`, `archive_encrypted`, `archive_solid_unsupported`, `archive_video_not_found`, `archive_headers_invalid`

Pending requirements:

- persist richer non-RAR4 and more complex multi-volume/header metadata
- expand archive-aware reader behavior beyond contiguous span validation
