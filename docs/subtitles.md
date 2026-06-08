# Subtitles

Subtitle publication now has a manual path plus provider-backed search/download paths.

Implemented:

- list persisted subtitle files with `GET /api/subtitles/{libraryItemId}`
- list persisted subtitle candidates with `GET /api/subtitle-candidates/{libraryItemId}`
- search SubDL or OpenSubtitles subtitle candidates with `POST /api/subtitles/{libraryItemId}/search`
- download one stored subtitle candidate with `POST /api/subtitle-candidates/{id}/download`
- upload manual `.srt` or `.vtt` subtitle files with `POST /api/subtitles/{libraryItemId}/upload`
- delete one persisted subtitle language/provider set with `DELETE /api/subtitle-files/{id}`
- publish uploaded subtitle files next to the current media library symlink targets
- republish persisted subtitle files when media publications are rebuilt or manually republished
- trigger non-blocking provider subtitle search after media publication and republish
- automatically download and publish the best provider candidate after background search when the item has no stored subtitles yet
- persist published subtitle rows in `subtitle_files`
- persist provider search results in `subtitle_candidates`
- expose manual subtitle upload controls in the library page
- expose provider candidate search and download controls in the library page

Current behavior:

- provider is recorded as `manual`
- one uploaded subtitle file is replicated across all published media paths for the library item
- language is operator-supplied and appended as `<basename>.<lang>.srt` or `.vtt`
- persisted subtitle rows are replayed onto the current library paths after startup publication rebuilds
- deleting one subtitle row removes the full replicated sidecar set for that provider/language group
- SubDL search uses library metadata plus configured preferred languages and stores unpacked raw subtitle-file candidates
- OpenSubtitles search uses authenticated API access with library metadata and returns download-by-`file_id` candidates
- candidate ranking now prefers exact language order, exact episode matches over season packs, matching title/year tokens, and canonical `sXXeYY`/`2x03` release text
- when candidates otherwise tie closely, provider-aware score bias now prefers OpenSubtitles slightly ahead of SubDL
- provider download now supports direct raw `.srt` / `.vtt` files and zip-only subtitle bundles
- zip downloads are unpacked in memory and the best subtitle file is selected by language plus release/title filename similarity
- automatic provider search runs asynchronously after publish/republish and does not block media availability
- automatic provider search now also auto-downloads the top-ranked candidate when no subtitle files are already stored for that item
- if the highest-ranked subtitle candidate fails to download or publish, automatic acquisition now falls through to the next ranked candidate
- existing stored subtitle rows prevent automatic provider downloads, so manual/operator-published subtitles remain authoritative

Configured providers:

- SubDL
- OpenSubtitles

Preferred languages:

- `nl`
- `en`

Still pending:

- broader subtitle candidate ranking heuristics
- broader packed subtitle archive handling beyond simple zip bundles
