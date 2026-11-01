import type { FieldError } from "../../api/rules";

/** What a refused save says about each part of the form. */
export interface SaveProblems {
  /** What is wrong with the reason, if anything. */
  reason?: string;
  /** What is wrong with the YAML, each with its path in the YAML in front when it has one. */
  body: string[];
}

const BODY_FIELD = "body";
const BODY_PREFIX = `${BODY_FIELD}.`;
const REASON_FIELD = "reason";

/** Sorts the fields of a refused save into the reason and the problems of the YAML. */
export function saveProblems(fields: readonly FieldError[]): SaveProblems {
  const out: SaveProblems = { body: [] };

  for (const { field, message } of fields) {
    if (field === REASON_FIELD) {
      out.reason = message;
    } else if (field.startsWith(BODY_PREFIX)) {
      out.body.push(`${field.slice(BODY_PREFIX.length)}: ${message}`);
    } else if (field === BODY_FIELD) {
      out.body.push(message);
    } else {
      out.body.push(field === "" ? message : `${field}: ${message}`);
    }
  }

  return out;
}
