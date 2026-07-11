// BackgroundTaskEvent is the shape of messages pushed over the shared SSE
// connection (see subscribeEvents in api.ts) for background task completion
// (search, republish, subtitle jobs, cache prune, etc). `kind` identifies
// which task fired; the rest of the fields vary by kind, so pages narrow on
// `kind` before reading task-specific fields.
export type BackgroundTaskEvent = {
  kind?: string;
  libraryItemId?: number;
  [key: string]: unknown;
};
