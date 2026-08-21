/**
 * Copies text to the clipboard, falling back to the legacy
 * selection-based execCommand approach when navigator.clipboard is
 * unavailable -- it's only exposed in secure contexts (HTTPS or
 * localhost), so it's undefined when the app is reached over plain HTTP
 * on a LAN address.
 */
export async function copyToClipboard(text: string): Promise<void> {
  if (navigator.clipboard) {
    await navigator.clipboard.writeText(text);
    return;
  }
  const textarea = document.createElement('textarea');
  textarea.value = text;
  textarea.style.position = 'fixed';
  textarea.style.opacity = '0';
  document.body.appendChild(textarea);
  textarea.select();
  const copied = document.execCommand('copy');
  textarea.remove();
  if (!copied) {
    throw new Error('Copy to clipboard failed');
  }
}
