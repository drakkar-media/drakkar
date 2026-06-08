import type { LibraryItem } from '$lib/types';

export const ACTIVE_STATES = ['searching','ranking','selected','fetching_nzb','indexing','preflight','publishing','downloading'];

// Status system matches the reference project's Library.tsx groupStatus() exactly:
//   available   → "Completed (Monitored)"   — green  #22c55e  order 0
//   partial     → "Completed (Unmonitored)" — blue   #5f98ff  order 1
//   active      → "Downloading"             — purple #8b5cf6  order 2
//   missing     → "Missing"                 — red    #ff5b5b  order 3
// "unreleased" reuses the blue slot for items not yet searched (queued by Seerr).
// "failed" collapses into "missing" since both need operator action.

export type ItemStatus = 'available' | 'partial' | 'active' | 'missing' | 'failed' | 'unreleased';

export const STATUS_ORDER: Record<ItemStatus, number> = {
  available:  0,
  partial:    1,
  active:     2,
  unreleased: 2,  // same tier as active — waiting but not yet downloading
  missing:    3,
  failed:     3,  // same tier as missing — needs retry
};

export function itemStatus(item: LibraryItem): ItemStatus {
  const s = item.queueState ?? '';
  if (ACTIVE_STATES.includes(s)) return 'active';
  if (s === 'failed') return 'missing';   // all failures collapse to "Missing" like reference
  if (s === 'requested') return 'unreleased'; // queued by Seerr, not yet searched
  if (item.available) {
    const isTv = item.mediaType === 'tv' || item.mediaType === 'episode';
    // 'partial' when missingCount > 0 (TMDB total known and some episodes still missing)
    if (isTv && (item.missingCount ?? 0) > 0) return 'partial';
    return 'available';
  }
  // When missingCount > 0 but not available yet → shows episodes downloaded vs total
  if ((item.missingCount ?? 0) > 0 && (item.availableCount ?? 0) > 0) return 'partial';
  return 'missing';
}
