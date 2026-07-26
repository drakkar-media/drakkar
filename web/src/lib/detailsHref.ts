/**
 * Minimal identity needed to build a details page link — a subset shared by
 * `LibraryItem`, `DiscoverMediaItem`, and similar list-row shapes.
 */
type DetailsHrefItem = {
  mediaType: string;
  title: string;
  year?: number | null;
  tmdbId?: number | null;
  imdbId?: string | null;
};

// Strips apostrophes rather than collapsing them to a hyphen so "Don't"
// becomes "dont" instead of "don-t", keeping slugs closer to the title.
function slugify(value: string) {
  return value.toLowerCase().replace(/['’]/g, '').replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'title';
}

/**
 * Builds the `/details/:mediaType/:idSlug` route for a media item.
 *
 * The slug leads with the TMDB ID when available, falling back to the IMDB
 * ID or, for items with neither, a title-only slug — in the no-ID case the
 * raw title and year are also passed as query params so the details route
 * can look the item up. `idFromSlug` reverses the ID portion of the slug.
 *
 * @param item - Media item to link to.
 * @returns A relative URL to the item's details page.
 */
export function detailsHref(item: DetailsHrefItem) {
  const mediaType = item.mediaType === 'tv' || item.mediaType === 'episode' ? 'tv' : 'movie';
  const id = item.tmdbId ?? item.imdbId;
  const idSlug = id ? `${id}-${slugify(item.title)}` : slugify(item.title);
  const params = new URLSearchParams();
  if (!item.tmdbId && item.imdbId) params.set('imdbId', item.imdbId);
  if (!id) {
    params.set('title', item.title);
    if (item.year) params.set('year', String(item.year));
  }
  return `/details/${mediaType}/${idSlug}${params.toString() ? `?${params.toString()}` : ''}`;
}

/**
 * Extracts the leading ID segment from a details-page slug produced by
 * {@link detailsHref}.
 *
 * @param idSlug - The `:idSlug` route param (e.g. `"603-the-matrix"`).
 * @returns The ID portion, or `undefined` if `idSlug` is missing or the
 * segment before the first hyphen is empty (a title-only slug with no ID).
 */
export function idFromSlug(idSlug?: string) {
  if (!idSlug) return undefined;
  return idSlug.split('-')[0] || undefined;
}
