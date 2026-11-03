import { MAX_TOKEN_NAME_CHARS, type NewToken } from "../../api/tokens";
import { hasRole, type Role } from "../../roles";

const ROLES_LOW_TO_HIGH: readonly Role[] = ["viewer", "responder", "approver", "admin"];

/** The expiry presets, in days from now. The last one is the longest a token may last. */
export const EXPIRY_PRESET_DAYS = [30, 90, 180, 365] as const;

/** The preset the form starts on. */
export const DEFAULT_EXPIRY_DAYS = 90;

/** The value of the expiry choice that lets the person pick a date. */
export const EXPIRY_DATE_CHOICE = "date";

const MAX_LIFETIME_DAYS = 365;
const MS_PER_HOUR = 3_600_000;
const HOURS_PER_DAY = 24;
const END_OF_DAY = { hours: 23, minutes: 59, seconds: 59 } as const;

/**
 * The latest moment after now that the form sends: a year and 23 hours. The
 * API allows 366 days, so an end of day cannot pass it, whatever a clock
 * change does to the length of a day.
 */
const LATEST_AFTER_NOW_MS = MAX_LIFETIME_DAYS * HOURS_PER_DAY * MS_PER_HOUR + (HOURS_PER_DAY - 1) * MS_PER_HOUR;

const DATE_TEXT = /^(\d{4})-(\d{2})-(\d{2})$/;

/** What the person has entered in the create form. expiry is a preset in days, or EXPIRY_DATE_CHOICE with the day in date. */
export interface TokenFormValues {
  name: string;
  role: Role;
  expiry: string;
  /** A day as a date input writes it, such as 2027-04-01. */
  date: string;
}

/** The problem with each field of the form, in words. A field with none has no entry. */
export interface TokenFormErrors {
  name?: string;
  role?: string;
  expiresAt?: string;
}

/** The roles a token may have for someone with role: their own and every one below it, lowest first. */
export function rolesUpTo(role: Role): Role[] {
  return ROLES_LOW_TO_HIGH.filter((candidate) => hasRole(role, candidate));
}

/** The first and last day a date may be picked, as a date input writes them: today, and a year from today. */
export function dateLimits(now: Date): { min: string; max: string } {
  const latest = new Date(now.getFullYear(), now.getMonth(), now.getDate() + MAX_LIFETIME_DAYS);

  return { min: dateText(now), max: dateText(latest) };
}

/** The body to send for the form, or the problems with it. Nothing is sent when there are problems. */
export function checkTokenForm(values: TokenFormValues, allowed: readonly Role[], now: Date): { body: NewToken } | { errors: TokenFormErrors } {
  const errors: TokenFormErrors = {};
  const name = values.name.trim();

  if (name === "") {
    errors.name = "Enter a name.";
  } else if ([...name].length > MAX_TOKEN_NAME_CHARS) {
    errors.name = `The name can have at most ${MAX_TOKEN_NAME_CHARS} characters.`;
  }

  if (!allowed.includes(values.role)) {
    errors.role = "Choose a role from the list.";
  }

  const expiry = expiresAtOf(values, now);
  if ("error" in expiry) {
    errors.expiresAt = expiry.error;
  }

  if (Object.keys(errors).length > 0 || "error" in expiry) {
    return { errors };
  }

  return { body: { name, role: values.role, expiresAt: expiry.expiresAt } };
}

function expiresAtOf(values: TokenFormValues, now: Date): { expiresAt: string } | { error: string } {
  if (values.expiry !== EXPIRY_DATE_CHOICE) {
    const days = EXPIRY_PRESET_DAYS.find((preset) => String(preset) === values.expiry);
    if (days === undefined) {
      return { error: "Choose how long the token lasts." };
    }

    return { expiresAt: new Date(now.getTime() + days * HOURS_PER_DAY * MS_PER_HOUR).toISOString() };
  }

  const match = DATE_TEXT.exec(values.date);
  if (!match) {
    return { error: "Pick the last day the token works." };
  }

  const { min, max } = dateLimits(now);
  if (values.date < min) {
    return { error: "Pick today or a later day." };
  }

  if (values.date > max) {
    return { error: `Pick a day on or before ${max}. A token lasts at most a year.` };
  }

  const endOfDay = new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]), END_OF_DAY.hours, END_OF_DAY.minutes, END_OF_DAY.seconds);
  const latest = now.getTime() + LATEST_AFTER_NOW_MS;

  return { expiresAt: new Date(Math.min(endOfDay.getTime(), latest)).toISOString() };
}

function dateText(date: Date): string {
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");

  return `${date.getFullYear()}-${month}-${day}`;
}
