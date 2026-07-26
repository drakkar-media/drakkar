import { redirect } from '@sveltejs/kit';

/**
 * Redirects the site root to `/dashboard`, which is the app's actual landing
 * page.
 *
 * @throws {Redirect} Always — a 307 redirect to `/dashboard`.
 */
export function load() {
  throw redirect(307, '/dashboard');
}
