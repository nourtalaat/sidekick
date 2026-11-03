import type { RuntimeRule } from "../../api/config";

/** A secret reference has exactly these two string keys. */
const SECRET_REF_KEYS = ["key", "name"];

export interface SecretRef {
  name: string;
  key: string;
}

/** Whether value is a plain object, not a list and not null. */
export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Whether value is an object with exactly the string keys name and key: a reference to a Secret, never a value. */
export function isSecretRef(value: unknown): value is SecretRef {
  if (!isRecord(value)) {
    return false;
  }

  const keys = Object.keys(value).sort();

  return keys.length === SECRET_REF_KEYS.length && keys.every((k, i) => k === SECRET_REF_KEYS[i]) && typeof value["name"] === "string" && typeof value["key"] === "string";
}

/** Whether text contains needle, ignoring case. An empty needle matches everything. */
export function matches(text: string, needle: string): boolean {
  return text.toLowerCase().includes(needle.toLowerCase());
}

/**
 * Narrows a config value to what matches needle, a plain case-insensitive
 * text. A map keeps an entry whole when its key matches, or else the part of
 * its value that matches; a list keeps the items that match; a secret reference
 * matches on its name or key. Returns undefined when nothing matches.
 */
export function filterValue(value: unknown, needle: string): unknown {
  if (isSecretRef(value)) {
    return matches(value.name, needle) || matches(value.key, needle) ? value : undefined;
  }

  if (Array.isArray(value)) {
    const kept = value.map((item) => filterValue(item, needle)).filter((item) => item !== undefined);

    return kept.length > 0 ? kept : undefined;
  }

  if (isRecord(value)) {
    const kept: Record<string, unknown> = {};
    for (const [key, item] of Object.entries(value)) {
      const narrowed = matches(key, needle) ? item : filterValue(item, needle);
      if (narrowed !== undefined) {
        kept[key] = narrowed;
      }
    }

    return Object.keys(kept).length > 0 ? kept : undefined;
  }

  if (value === null || value === undefined) {
    return undefined;
  }

  return matches(String(value), needle) ? value : undefined;
}

/**
 * Narrows the entries of a map, such as the top-level keys of the file config,
 * to those whose key matches (kept whole) or whose value has a match (kept in
 * part), in the order the map has them.
 */
export function filterEntries(entries: Record<string, unknown>, needle: string): [string, unknown][] {
  if (needle === "") {
    return Object.entries(entries);
  }

  const kept: [string, unknown][] = [];
  for (const [key, value] of Object.entries(entries)) {
    const narrowed = matches(key, needle) ? value : filterValue(value, needle);
    if (narrowed !== undefined) {
      kept.push([key, narrowed]);
    }
  }

  return kept;
}

/**
 * Narrows runtime rules to those that match needle. A rule whose name or last
 * editor matches is kept whole; any other keeps the part of its config that matches.
 */
export function filterRules(rules: readonly RuntimeRule[], needle: string): RuntimeRule[] {
  if (needle === "") {
    return [...rules];
  }

  const kept: RuntimeRule[] = [];
  for (const rule of rules) {
    if (matches(rule.name, needle) || matches(rule.updatedBy, needle)) {
      kept.push(rule);
      continue;
    }

    const config = filterValue(rule.config, needle);
    if (isRecord(config)) {
      kept.push({ ...rule, config });
    }
  }

  return kept;
}
