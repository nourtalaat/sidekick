/**
 * url when it is an absolute http or https address, and undefined otherwise.
 * Text and links from alerts, sources and the model are not trusted: a link
 * to anything else, such as javascript: or data:, is shown as plain text.
 */
export function safeHttpUrl(url: string | undefined): string | undefined {
  if (!url) {
    return undefined;
  }

  try {
    const parsed = new URL(url);

    return (parsed.protocol === "http:" || parsed.protocol === "https:") && parsed.host !== "" ? parsed.href : undefined;
  } catch {
    return undefined;
  }
}
