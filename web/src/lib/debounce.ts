/**
 * Collapses a burst of rapid calls (e.g. several SSE messages arriving
 * within milliseconds of each other) into a single trailing call, so pages
 * that refetch on "something changed" events don't issue a fresh network
 * request per message when many arrive in quick succession.
 *
 * @param fn - Function to invoke after the burst settles.
 * @param waitMs - Quiet period required, in milliseconds, before `fn` fires.
 * Each call resets the timer.
 * @returns A debounced wrapper around `fn`.
 */
export function debounce<Args extends unknown[]>(fn: (...args: Args) => void, waitMs: number): (...args: Args) => void {
  let timer: ReturnType<typeof setTimeout> | undefined;
  return (...args: Args) => {
    clearTimeout(timer);
    timer = setTimeout(() => fn(...args), waitMs);
  };
}
