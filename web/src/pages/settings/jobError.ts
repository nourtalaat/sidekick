/** How many lines of a job's last error show before "Show all". */
export const ERROR_PREVIEW_LINES = 3;

/** How many characters of a job's last error show before "Show all". */
export const ERROR_PREVIEW_CHARS = 200;

const ELLIPSIS = "…";

/** The start of an error text: at most the first lines and characters, with whether anything was left out. */
export function errorPreview(text: string): { text: string; cut: boolean } {
  const lines = text.split("\n");
  let shown = lines.slice(0, ERROR_PREVIEW_LINES).join("\n");
  let cut = lines.length > ERROR_PREVIEW_LINES;

  const characters = Array.from(shown);
  if (characters.length > ERROR_PREVIEW_CHARS) {
    shown = characters.slice(0, ERROR_PREVIEW_CHARS).join("");
    cut = true;
  }

  return { text: cut ? `${shown.trimEnd()}${ELLIPSIS}` : shown, cut };
}
