/**
 * Svelte action for a modal dialog container: on mount, moves focus into
 * `node` (its first focusable descendant, or `node` itself if none) and
 * confines Tab/Shift+Tab cycling to `node`'s own focusable descendants; on
 * destroy, restores focus to whatever was focused before the modal opened.
 *
 * Without this, a modal opened via a click (the trigger button stays
 * focused, now hidden behind the overlay) never actually receives keyboard
 * events -- Escape/Tab handlers on the modal's own container only fire for
 * events that bubble up through it, which never happens if focus was never
 * moved inside it in the first place.
 */
export function trapFocus(node: HTMLElement) {
  const previouslyFocused = document.activeElement as HTMLElement | null;

  function focusableElements(): HTMLElement[] {
    return Array.from(
      node.querySelectorAll<HTMLElement>(
        'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
      )
    ).filter((el) => el.offsetParent !== null);
  }

  (focusableElements()[0] ?? node).focus();

  function onKeydown(event: KeyboardEvent) {
    if (event.key !== 'Tab') return;
    const elements = focusableElements();
    if (elements.length === 0) return;
    const first = elements[0];
    const last = elements[elements.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  node.addEventListener('keydown', onKeydown);

  return {
    destroy() {
      node.removeEventListener('keydown', onKeydown);
      previouslyFocused?.focus();
    }
  };
}
