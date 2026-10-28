/** A time of the API as the reader's local date and time. */
export function formatTime(iso: string): string {
  const date = new Date(iso);

  return Number.isNaN(date.getTime()) ? iso : date.toLocaleString();
}
