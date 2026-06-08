type DetailsHrefItem = {
  mediaType: string;
  title: string;
  year?: number | null;
  tmdbId?: number | null;
  imdbId?: string | null;
};

function slugify(value: string) {
  return value.toLowerCase().replace(/['’]/g, '').replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'title';
}

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

export function idFromSlug(idSlug?: string) {
  if (!idSlug) return undefined;
  return idSlug.split('-')[0] || undefined;
}
