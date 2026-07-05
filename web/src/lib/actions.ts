import { toastError, toastSuccess } from './toast';

// Wraps the "set a working flag, call the API, toast the result, reload,
// toast on failure, always clear the working flag" shape that was
// copy-pasted across most page action handlers (confirm dialogs before
// destructive actions are the caller's responsibility — do that before
// calling run).
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
