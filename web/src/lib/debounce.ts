// debounce collapses a burst of rapid calls (e.g. several SSE messages
// arriving within milliseconds of each other) into a single trailing call,
// so pages that refetch on "something changed" events don't issue a fresh
// network request per message when many arrive in quick succession.
export function debounce<Args extends unknown[]>(fn: (...args: Args) => void, waitMs: number): (...args: Args) => void {
  let timer: ReturnType<typeof setTimeout> | undefined;
  return (...args: Args) => {
    clearTimeout(timer);
    timer = setTimeout(() => fn(...args), waitMs);
  };
}
