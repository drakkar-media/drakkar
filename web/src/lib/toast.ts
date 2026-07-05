import { writable } from 'svelte/store';

export type ToastTone = 'info' | 'success' | 'error';

export type ToastItem = {
  id: number;
  message: string;
  tone: ToastTone;
};

const items = writable<ToastItem[]>([]);

let nextID = 1;

function push(message: string, tone: ToastTone = 'info', ttlMs = 4000) {
  const id = nextID++;
  items.update((current) => [...current, { id, message, tone }]);
  const timer = window.setTimeout(() => dismiss(id), ttlMs);
  return () => {
    window.clearTimeout(timer);
    dismiss(id);
  };
}

export function dismiss(id: number) {
  items.update((current) => current.filter((item) => item.id !== id));
}

export function toastSuccess(message: string) {
  return push(message, 'success');
}

export function toastError(message: string) {
  return push(message, 'error', 6000);
}

export const toasts = {
  subscribe: items.subscribe
};
