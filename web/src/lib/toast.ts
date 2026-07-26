import { writable } from 'svelte/store';

export type ToastTone = 'info' | 'success' | 'error';

export type ToastItem = {
  id: number;
  message: string;
  tone: ToastTone;
};

const items = writable<ToastItem[]>([]);

let nextID = 1;

/**
 * Appends a toast and schedules its automatic dismissal.
 *
 * @param message - Text to display.
 * @param tone - Visual style/severity of the toast.
 * @param ttlMs - Milliseconds before the toast auto-dismisses.
 * @returns A function that cancels the auto-dismiss timer and immediately
 * dismisses the toast; callers may invoke it for an early, manual dismiss.
 */
function push(message: string, tone: ToastTone = 'info', ttlMs = 4000) {
  const id = nextID++;
  items.update((current) => [...current, { id, message, tone }]);
  const timer = window.setTimeout(() => dismiss(id), ttlMs);
  return () => {
    window.clearTimeout(timer);
    dismiss(id);
  };
}

/**
 * Removes a toast by ID. Safe to call after the toast has already been
 * dismissed (e.g. by its own TTL).
 */
export function dismiss(id: number) {
  items.update((current) => current.filter((item) => item.id !== id));
}

/** Shows a success toast with the default TTL. */
export function toastSuccess(message: string) {
  return push(message, 'success');
}

/** Shows an error toast with an extended TTL, since failures need more time to read. */
export function toastError(message: string) {
  return push(message, 'error', 6000);
}

/**
 * Contains the toasts currently visible to the user.
 *
 * The store is populated exclusively by {@link toastSuccess} and
 * {@link toastError} (and internally by {@link push}/{@link dismiss}).
 * Components should subscribe to render the toast list but should not
 * mutate it directly.
 */
export const toasts = {
  subscribe: items.subscribe
};
