import { toastError, toastSuccess } from './toast';

/**
 * Runs an API call with the "set a working flag, call the API, toast the
 * result, run a follow-up, always clear the working flag" shape that was
 * copy-pasted across most page action handlers.
 *
 * Confirming destructive actions (e.g. via {@link confirmed}) is the
 * caller's responsibility and must happen before invoking this function.
 *
 * @param fn - The API call to perform.
 * @param opts.setWorking - Toggled true before `fn` runs and false again once
 * it settles, regardless of outcome.
 * @param opts.successMessage - Builds the toast message from `fn`'s result.
 * @param opts.afterSuccess - Optional follow-up (e.g. reloading page data),
 * run only when `fn` succeeds.
 * @returns The result of `fn`, or `undefined` if it threw — the error is
 * toasted rather than rethrown.
 */
export async function runAction<T>(
  fn: () => Promise<T>,
  opts: {
    setWorking: (working: boolean) => void;
    successMessage: (result: T) => string;
    afterSuccess?: (result: T) => void | Promise<void>;
  }
): Promise<T | undefined> {
  opts.setWorking(true);
  try {
    const result = await fn();
    toastSuccess(opts.successMessage(result));
    if (opts.afterSuccess) await opts.afterSuccess(result);
    return result;
  } catch (err) {
    toastError(err instanceof Error ? err.message : String(err));
    return undefined;
  } finally {
    opts.setWorking(false);
  }
}

/**
 * Shared guard for destructive actions, replacing the
 * `if (typeof window !== 'undefined' && !window.confirm(message)) return;`
 * line copy-pasted at ~21 call sites.
 *
 * @param message - Confirmation prompt shown to the user.
 * @returns Whether the action should proceed. Confirmation is skipped (and
 * `true` returned) only when `window` is unavailable (SSR) — never the case
 * for these client-only handlers.
 */
export function confirmed(message: string): boolean {
  return typeof window === 'undefined' || window.confirm(message);
}
