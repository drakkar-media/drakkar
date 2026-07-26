/**
 * Formats a byte count as a human-readable size (e.g. `"512 MiB"`).
 *
 * Scales through binary (1024-based) units up to TiB. Values under 10 in the
 * chosen unit keep one decimal place; larger values and whole bytes are
 * rounded to an integer to avoid noisy precision like `"1024.0 B"`.
 *
 * @param value - Size in bytes. `0` (or falsy) renders as `"0 B"`.
 */
export function bytes(value: number): string {
  if (!value) return '0 B';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit++;
  }
  return `${size.toFixed(size >= 10 || unit === 0 ? 0 : 1)} ${units[unit]}`;
}

/**
 * Formats an ISO timestamp using the browser's locale conventions.
 *
 * @param value - ISO timestamp string, or `undefined`/empty for "n/a".
 * @returns The locale-formatted date/time, `"n/a"` if `value` is missing, or
 * the original string unchanged if it doesn't parse as a valid date.
 */
export function dateTime(value?: string): string {
  if (!value) return 'n/a';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

/**
 * Renders a free-text value, substituting `"none"` for empty or
 * whitespace-only input so UI fields never render blank.
 */
export function sentence(value?: string): string {
  return value && value.trim() ? value : 'none';
}
