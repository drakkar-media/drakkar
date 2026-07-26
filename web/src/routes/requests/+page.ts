import { redirect } from '@sveltejs/kit';

/**
 * Redirects /requests to /library — requests are now managed from the library page.
 */
export function load() {
  throw redirect(307, '/library');
}
