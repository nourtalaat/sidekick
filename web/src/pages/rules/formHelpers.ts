import type { FieldError } from "../../api/rules";

/** The errors of a form: a message for each field it shows, and the messages it has no field for. */
export interface SortedErrors {
  byField: Record<string, string>;
  other: string[];
}

const LIST_INDEX = /\[\d+\]/g;

/**
 * Sorts the problems the server found by the form field they belong to. A
 * field of the API is a path such as sources[1] or match.intake[0]; the
 * form has one input for the whole list, so the index is dropped. A problem
 * about a field the form does not have is kept in other, with its path.
 */
export function sortErrors(problems: readonly FieldError[], known: readonly string[]): SortedErrors {
  const byField: Record<string, string> = {};
  const other: string[] = [];

  for (const { field, message } of problems) {
    const key = field.replace(LIST_INDEX, "");

    if (known.includes(key)) {
      byField[key] = byField[key] ? `${byField[key]} ${message}` : message;
    } else if (field === "") {
      other.push(message);
    } else {
      other.push(`${field}: ${message}`);
    }
  }

  return { byField, other };
}

const LIST_SEPARATOR = /[,\n]/;

/** The items of a list written with commas or one to a line, without blanks. */
export function splitList(text: string): string[] {
  return text
    .split(LIST_SEPARATOR)
    .map((item) => item.trim())
    .filter((item) => item !== "");
}

/** The lines of a text, without blanks. For patterns, which can hold commas. */
export function splitLines(text: string): string[] {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "");
}

/** A list written one item to a line. */
export function joinLines(items: readonly string[] | undefined): string {
  return (items ?? []).join("\n");
}

/** A list written with commas. */
export function joinList(items: readonly string[] | undefined): string {
  return (items ?? []).join(", ");
}

/** The labels written as name=value, one to a line, or an error that says which line is wrong. */
export function parseLabels(text: string): { labels: Record<string, string>; error?: string } {
  const labels: Record<string, string> = {};

  for (const line of splitLines(text)) {
    const at = line.indexOf("=");
    if (at <= 0) {
      return { labels, error: `"${line}" is not written as name=value.` };
    }

    labels[line.slice(0, at).trim()] = line.slice(at + 1).trim();
  }

  return { labels };
}

/** The labels written as name=value, one to a line, sorted by name. */
export function formatLabels(labels: Record<string, string> | undefined): string {
  return Object.entries(labels ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([name, value]) => `${name}=${value}`)
    .join("\n");
}

const WHOLE_NUMBER = /^-?\d+$/;
const DECIMAL_NUMBER = /^-?\d+(\.\d+)?$/;

/** A whole number written in a field, undefined when it is empty, or null when it is not a number. */
export function parseWhole(text: string): number | undefined | null {
  const trimmed = text.trim();
  if (trimmed === "") {
    return undefined;
  }

  return WHOLE_NUMBER.test(trimmed) ? Number(trimmed) : null;
}

/** A number with decimals written in a field, undefined when it is empty, or null when it is not a number. */
export function parseDecimal(text: string): number | undefined | null {
  const trimmed = text.trim();
  if (trimmed === "") {
    return undefined;
  }

  return DECIMAL_NUMBER.test(trimmed) ? Number(trimmed) : null;
}

/** The list, or undefined when it is empty, so an empty list is left out of a request. */
export function orUndefined<T>(items: T[]): T[] | undefined {
  return items.length > 0 ? items : undefined;
}

/** The text, or undefined when it is blank. */
export function textOrUndefined(text: string): string | undefined {
  const trimmed = text.trim();

  return trimmed === "" ? undefined : trimmed;
}
