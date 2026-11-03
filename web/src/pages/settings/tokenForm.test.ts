import { describe, expect, it } from "vitest";
import type { Role } from "../../roles";
import { checkTokenForm, dateLimits, DEFAULT_EXPIRY_DAYS, EXPIRY_DATE_CHOICE, EXPIRY_PRESET_DAYS, rolesUpTo, type TokenFormValues } from "./tokenForm";

const NOW = new Date(2026, 10, 3, 10, 0, 0);
const MS_PER_DAY = 86_400_000;
const ALL_ROLES = rolesUpTo("admin");

function values(over: Partial<TokenFormValues> = {}): TokenFormValues {
  return { name: "nightly bench", role: "viewer", expiry: String(DEFAULT_EXPIRY_DAYS), date: "", ...over };
}

function errorFields(result: ReturnType<typeof checkTokenForm>): string[] {
  return "errors" in result ? Object.keys(result.errors) : [];
}

describe("rolesUpTo", () => {
  it.each([
    { role: "viewer", want: ["viewer"] },
    { role: "responder", want: ["viewer", "responder"] },
    { role: "approver", want: ["viewer", "responder", "approver"] },
    { role: "admin", want: ["viewer", "responder", "approver", "admin"] },
  ] as const)("offers $want to $role", ({ role, want }) => {
    expect(rolesUpTo(role)).toEqual(want);
  });
});

describe("dateLimits", () => {
  it.each([
    { name: "an ordinary day", now: new Date(2026, 10, 3, 10), min: "2026-11-03", max: "2027-11-03" },
    { name: "just before midnight", now: new Date(2026, 10, 3, 23, 59), min: "2026-11-03", max: "2027-11-03" },
    { name: "a year that ends on a leap day", now: new Date(2023, 2, 1, 10), min: "2023-03-01", max: "2024-02-29" },
    { name: "the end of a year", now: new Date(2026, 11, 31, 10), min: "2026-12-31", max: "2027-12-31" },
  ])("is today and 365 days later on $name", ({ now, min, max }) => {
    expect(dateLimits(now)).toEqual({ min, max });
  });
});

describe("checkTokenForm", () => {
  it.each(EXPIRY_PRESET_DAYS)("sends now plus %i days for that preset", (days) => {
    const result = checkTokenForm(values({ expiry: String(days) }), ALL_ROLES, NOW);

    expect(result).toEqual({ body: { name: "nightly bench", role: "viewer", expiresAt: new Date(NOW.getTime() + days * MS_PER_DAY).toISOString() } });
  });

  it("trims the name", () => {
    expect(checkTokenForm(values({ name: "  bench  " }), ALL_ROLES, NOW)).toMatchObject({ body: { name: "bench" } });
  });

  const refused: { name: string; over: Partial<TokenFormValues>; field: string; allowed?: Role[] }[] = [
    { name: "a name that is empty", over: { name: "" }, field: "name" },
    { name: "a name of only spaces", over: { name: "   " }, field: "name" },
    { name: "a name of 101 characters", over: { name: "x".repeat(101) }, field: "name" },
    { name: "a role above the offered ones", over: { role: "admin" }, field: "role", allowed: ["viewer", "responder"] },
    { name: "an expiry that is not a preset", over: { expiry: "400" }, field: "expiresAt" },
    { name: "a date with no day", over: { expiry: EXPIRY_DATE_CHOICE, date: "" }, field: "expiresAt" },
    { name: "a day in the past", over: { expiry: EXPIRY_DATE_CHOICE, date: "2026-11-02" }, field: "expiresAt" },
    { name: "a day more than a year away", over: { expiry: EXPIRY_DATE_CHOICE, date: "2027-11-04" }, field: "expiresAt" },
  ];

  it.each(refused)("refuses $name", ({ over, field, allowed }) => {
    expect(errorFields(checkTokenForm(values(over), allowed ?? ALL_ROLES, NOW))).toEqual([field]);
  });

  it("counts characters and not bytes in the name", () => {
    expect(checkTokenForm(values({ name: "é".repeat(100) }), ALL_ROLES, NOW)).toHaveProperty("body");
    expect(checkTokenForm(values({ name: "é".repeat(101) }), ALL_ROLES, NOW)).toHaveProperty("errors");
  });

  it("sends the end of the chosen day, and accepts today and the last day", () => {
    const today = checkTokenForm(values({ expiry: EXPIRY_DATE_CHOICE, date: "2026-11-03" }), ALL_ROLES, NOW);
    expect(today).toMatchObject({ body: { expiresAt: new Date(2026, 10, 3, 23, 59, 59).toISOString() } });

    const last = checkTokenForm(values({ expiry: EXPIRY_DATE_CHOICE, date: "2027-11-03" }), ALL_ROLES, NOW);
    expect(last).toHaveProperty("body");
  });

  it("never sends a time more than the API's 366 days away, even for the last day", () => {
    const early = new Date(2026, 10, 3, 0, 0, 0);
    const result = checkTokenForm(values({ expiry: EXPIRY_DATE_CHOICE, date: "2027-11-03" }), ALL_ROLES, early);

    if (!("body" in result)) {
      throw new Error("want a body");
    }

    expect(new Date(result.body.expiresAt).getTime() - early.getTime()).toBeLessThanOrEqual(366 * MS_PER_DAY);
  });

  it("reports every problem at once", () => {
    expect(errorFields(checkTokenForm(values({ name: "", expiry: "1" }), ALL_ROLES, NOW))).toEqual(["name", "expiresAt"]);
  });
});
