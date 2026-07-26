/**
 * Root layout load configuration.
 *
 * Disables prerendering and server-side rendering for the entire app: every
 * route depends on client-side auth/session state resolved in
 * `+layout.svelte`'s `onMount`, which has no meaningful SSR equivalent given
 * the cookie-based auth check against the backend API.
 */
export const prerender = false;
export const ssr = false;
