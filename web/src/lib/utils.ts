import { clsx, type ClassValue } from 'clsx';
import { twMerge } from 'tailwind-merge';
import type { Snippet } from 'svelte';

/** Merges Tailwind class lists, letting later classes override earlier
 * conflicting ones (e.g. a caller's `class="text-sm"` prop overriding a
 * component's own default) -- the standard shadcn-svelte helper every
 * generated `ui/*` component imports. */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

/** Utility type generated shadcn-svelte components use so a caller can
 * `bind:ref` to the underlying DOM element. */
export type WithElementRef<T, E extends HTMLElement = HTMLElement> = T & {
  ref?: E | null;
};

export type WithoutChildren<T> = Omit<T, 'children'>;
export type WithoutChild<T> = Omit<T, 'child'>;
export type WithoutChildrenOrChild<T> = WithoutChildren<WithoutChild<T>>;

export type WithChildren<T> = T & { children?: Snippet };
