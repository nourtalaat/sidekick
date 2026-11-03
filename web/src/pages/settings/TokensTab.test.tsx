import { QueryClient } from "@tanstack/react-query";
import { fireEvent, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createQueryClient } from "../../api/queryClient";
import type { Token } from "../../api/tokens";
import { sampleConfig } from "../../test/config";
import { json, problem, renderApp } from "../../test/helpers";
import { ADMIN, recording, rowAt, validationProblem, type Recorded } from "../../test/rules";

const SECRET = `skt_${"k3Hq9".repeat(8)}`;
const NOW = new Date(2026, 10, 3, 10, 0, 0);
const MS_PER_DAY = 86_400_000;
const ROUTE = "/settings?tab=tokens";
const TOKENS_PATH = "/api/v1/tokens";

function token(id: string, over: Partial<Token> = {}): Token {
  return {
    id,
    name: id,
    role: "viewer",
    createdBy: "u-admin-1",
    createdAt: "2026-10-20T10:00:00Z",
    expiresAt: "2099-01-01T00:00:00Z",
    ...over,
  };
}

const NIGHTLY = token("0192d3a4-0000-7000-8000-0000000000a1", { name: "nightly bench", createdAt: "2026-10-20T10:00:00Z", lastUsedAt: "2026-11-01T08:30:00Z" });
const DEPLOY = token("0192d3a4-0000-7000-8000-0000000000a2", { name: "deploy hook", role: "responder", createdAt: "2026-10-25T10:00:00Z" });
const EXPIRED = token("0192d3a4-0000-7000-8000-0000000000a3", { name: "old import", role: "approver", createdAt: "2026-06-01T10:00:00Z", expiresAt: "2026-09-01T00:00:00Z" });
const REVOKED = token("0192d3a4-0000-7000-8000-0000000000a4", { name: "leaked key", role: "admin", createdAt: "2026-10-27T10:00:00Z", revokedAt: "2026-10-28T09:00:00Z" });

type CreateAnswer = (body: Record<string, unknown>) => Response;

/** A fake of the token calls that keeps its own list, so a refresh shows what the calls changed. */
function fakeTokens(initial: Token[], options: { creates?: CreateAnswer[]; revoke?: () => Response } = {}) {
  let tokens = [...initial];
  const creates = [...(options.creates ?? [])];

  const created = (body: Record<string, unknown>): Response => {
    const made = token(`0192d3a4-0000-7000-8000-0000000000b${tokens.length}`, { name: String(body["name"]), role: body["role"] as Token["role"], expiresAt: String(body["expiresAt"]), createdAt: "2026-11-03T10:00:00Z" });
    tokens = [made, ...tokens];

    return json(201, { token: made, secret: SECRET });
  };

  const { handler, seen } = recording((request: Recorded) => {
    if (request.path === "/api/v1/config") {
      return json(200, sampleConfig());
    }

    if (request.path === TOKENS_PATH && request.method === "GET") {
      return json(200, { tokens });
    }

    if (request.path === TOKENS_PATH && request.method === "POST") {
      return (creates.shift() ?? created)(request.body as Record<string, unknown>);
    }

    if (request.method === "DELETE" && request.path.startsWith(`${TOKENS_PATH}/`)) {
      const answer = options.revoke?.();
      if (answer) {
        return answer;
      }

      const id = request.path.slice(TOKENS_PATH.length + 1);
      tokens = tokens.map((t) => (t.id === id ? { ...t, revokedAt: "2026-11-03T10:00:00Z" } : t));

      return new Response(null, { status: 204 });
    }

    return problem(404, "Not found.");
  });

  return { handler, seen };
}

function open(initial: Token[], options: { creates?: CreateAnswer[]; revoke?: () => Response; queryClient?: QueryClient; route?: string } = {}) {
  const { handler, seen } = fakeTokens(initial, options);

  return { ...renderApp({ me: ADMIN, route: options.route ?? ROUTE, handler, queryClient: options.queryClient }), requests: seen };
}

function tableRows(): HTMLElement[] {
  return within(screen.getByRole("region", { name: "API tokens" })).getAllByRole("row").slice(1);
}

function posts(requests: Recorded[]): Recorded[] {
  return requests.filter((r) => r.method === "POST" && r.path === TOKENS_PATH);
}

async function fillName(user: ReturnType<typeof userEvent.setup>, name: string) {
  await user.type(await screen.findByRole("textbox", { name: "Name" }), name);
}

async function create(user: ReturnType<typeof userEvent.setup>, name: string) {
  await fillName(user, name);
  await user.click(screen.getByRole("button", { name: "Create token" }));
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(NOW);
});

afterEach(() => {
  vi.useRealTimers();
  localStorage.clear();
  sessionStorage.clear();
});

describe("the API tokens tab: the list", () => {
  it("is a tab of the Settings page after Config, and opens from the tab list", async () => {
    const user = userEvent.setup();
    open([NIGHTLY], { route: "/settings" });

    expect(await screen.findByRole("tab", { name: "Config" })).toHaveAttribute("aria-selected", "true");

    await user.click(screen.getByRole("tab", { name: "API tokens" }));

    expect(screen.getByRole("tab", { name: "API tokens" })).toHaveAttribute("aria-selected", "true");
    expect(await screen.findByRole("region", { name: "API tokens" })).toBeInTheDocument();
    expect(screen.getAllByRole("tab").map((t) => t.textContent)).toEqual(["Config", "API tokens", "Failed jobs"]);
  });

  it("lists active tokens first, then expired and revoked ones, each group newest first", async () => {
    open([EXPIRED, NIGHTLY, REVOKED, DEPLOY]);
    await screen.findByRole("region", { name: "API tokens" });

    const names = tableRows().map((row) => within(row).getAllByRole("rowheader")[0]?.textContent);

    expect(names).toEqual(["deploy hook", "nightly bench", "leaked key", "old import"]);
  });

  it("shows the columns, with the creator as a plain ID and Never for a token that was not used", async () => {
    open([NIGHTLY, DEPLOY]);
    await screen.findByRole("region", { name: "API tokens" });

    const headers = screen.getAllByRole("columnheader").map((h) => h.textContent);
    expect(headers).toEqual(["Name", "Role", "Created", "Expires", "Last used", "Status", "Actions"]);

    const deploy = within(rowAt(tableRows(), 0));
    const nightly = within(rowAt(tableRows(), 1));

    expect(deploy.getByText("Responder")).toBeInTheDocument();
    expect(deploy.getByText("u-admin-1").tagName).toBe("CODE");
    expect(deploy.getByText("Never")).toBeInTheDocument();
    expect(nightly.getByText("Viewer")).toBeInTheDocument();
    expect(nightly.queryByText("Never")).not.toBeInTheDocument();
    expect(rowAt(tableRows(), 1).querySelectorAll("time")).toHaveLength(3);
  });

  it("names expired and revoked rows in words, dims them and offers no Revoke button on them", async () => {
    open([NIGHTLY, EXPIRED, REVOKED]);
    await screen.findByRole("region", { name: "API tokens" });

    const [active, revoked, expired] = tableRows() as [HTMLElement, HTMLElement, HTMLElement];

    expect(within(active).getByText("Active")).toBeInTheDocument();
    expect(active).not.toHaveClass("sk-tokens__row--inactive");
    expect(within(active).getByRole("button", { name: "Revoke token nightly bench" })).toBeInTheDocument();

    expect(within(revoked).getByText("Revoked")).toBeInTheDocument();
    expect(within(revoked).getByText(/^At$/)).toBeInTheDocument();
    expect(revoked).toHaveClass("sk-tokens__row--inactive");
    expect(within(revoked).queryByRole("button")).not.toBeInTheDocument();

    expect(within(expired).getByText("Expired")).toBeInTheDocument();
    expect(expired).toHaveClass("sk-tokens__row--inactive");
    expect(within(expired).queryByRole("button")).not.toBeInTheDocument();
  });

  it("counts a token that is both past its expiry and revoked as revoked", async () => {
    open([token("t1", { name: "both", expiresAt: "2026-09-01T00:00:00Z", revokedAt: "2026-08-01T00:00:00Z" })]);
    await screen.findByRole("region", { name: "API tokens" });

    expect(within(rowAt(tableRows(), 0)).getByText("Revoked")).toBeInTheDocument();
    expect(within(rowAt(tableRows(), 0)).queryByText("Expired")).not.toBeInTheDocument();
  });

  it("shows a token name as plain text, never as HTML", async () => {
    open([token("t1", { name: "<b>bold</b> <img src=x onerror=alert(1)>" })]);
    await screen.findByRole("region", { name: "API tokens" });

    expect(screen.getByText("<b>bold</b> <img src=x onerror=alert(1)>")).toBeInTheDocument();
    expect(document.querySelector("img[src='x']")).toBeNull();
    expect(screen.getByRole("region", { name: "API tokens" }).querySelector("b")).toBeNull();
  });

  it("says the list is empty and points to the form", async () => {
    open([]);

    expect(await screen.findByRole("heading", { name: "No API tokens yet" })).toBeInTheDocument();
    expect(screen.getByText("Use the form above to create one.")).toBeInTheDocument();
    expect(screen.getByRole("form", { name: "Create an API token" })).toBeInTheDocument();
  });

  it("says Loading while the list is read", async () => {
    const { handler } = recording((request) => (request.path === TOKENS_PATH ? new Promise<Response>(() => undefined) : problem(404, "Not found.")));
    renderApp({ me: ADMIN, route: ROUTE, handler });

    expect(await screen.findByText("Loading")).toBeInTheDocument();
  });

  it("shows why the list could not be read and offers a retry", async () => {
    const user = userEvent.setup();
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let fail = true;
    const { handler } = recording((request) => {
      if (request.path === TOKENS_PATH) {
        return fail ? problem(500, "The database is down.") : json(200, { tokens: [NIGHTLY] });
      }

      return problem(404, "Not found.");
    });

    renderApp({ me: ADMIN, route: ROUTE, handler, queryClient });

    expect(await screen.findByText("The database is down.")).toBeInTheDocument();

    fail = false;
    await user.click(screen.getByRole("button", { name: "Try again" }));

    expect(await screen.findByRole("region", { name: "API tokens" })).toBeInTheDocument();
  });
});

describe("the API tokens tab: creating a token", () => {
  it("offers every role up to the admin's own and the expiry presets, and says a token can never approve", async () => {
    open([]);
    await screen.findByRole("textbox", { name: "Name" });

    const roleOptions = within(screen.getByRole("combobox", { name: "Role" })).getAllByRole("option");
    const expiryOptions = within(screen.getByRole("combobox", { name: "Expires" })).getAllByRole("option");

    expect(roleOptions.map((o) => o.textContent)).toEqual(["Viewer", "Responder", "Approver", "Admin"]);
    expect(expiryOptions.map((o) => o.textContent)).toEqual(["In 30 days", "In 90 days", "In 180 days", "In 365 days", "On a date"]);
    expect(screen.getByText(/It can never approve an action proposal\./)).toBeInTheDocument();
    expect(screen.getByText(/It needs no CSRF token\./)).toBeInTheDocument();
    expect(screen.getByText(/It is shown only once\./)).toBeInTheDocument();
  });

  it("sends the name, role and expiry with the CSRF header, shows the secret once with Copy, and reads the list again", async () => {
    const user = userEvent.setup();
    const { requests } = open([NIGHTLY]);

    await fillName(user, "  ci upload  ");
    await user.selectOptions(screen.getByRole("combobox", { name: "Role" }), "responder");
    await user.selectOptions(screen.getByRole("combobox", { name: "Expires" }), "180");
    await user.click(screen.getByRole("button", { name: "Create token" }));

    expect(await screen.findByText(SECRET)).toBeInTheDocument();

    const [sent] = posts(requests);
    expect(sent?.csrf).toBe("csrf-ada");
    expect(sent?.body).toEqual({ name: "ci upload", role: "responder", expiresAt: new Date(NOW.getTime() + 180 * MS_PER_DAY).toISOString() });

    expect(screen.getByRole("heading", { name: "Token created" })).toBeInTheDocument();
    expect(screen.getByText(/Copy it now\. It is not shown again\./)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy" })).toBeInTheDocument();
    expect(screen.getByText(SECRET).closest('[role="status"]')).toHaveFocus();

    expect(await screen.findByRole("rowheader", { name: "ci upload" })).toBeInTheDocument();
    expect(requests.filter((r) => r.method === "GET" && r.path === TOKENS_PATH).length).toBeGreaterThanOrEqual(2);
    expect(screen.getByRole("textbox", { name: "Name" })).toHaveValue("");
  });

  it.each([30, 90, 180, 365])("sends now plus %i days for that preset", async (days) => {
    const user = userEvent.setup();
    const { requests } = open([]);

    await fillName(user, "bench");
    await user.selectOptions(screen.getByRole("combobox", { name: "Expires" }), String(days));
    await user.click(screen.getByRole("button", { name: "Create token" }));
    await screen.findByText(SECRET);

    expect(posts(requests)[0]?.body).toMatchObject({ expiresAt: new Date(NOW.getTime() + days * MS_PER_DAY).toISOString() });
  });

  it("removes the secret when Done is pressed", async () => {
    const user = userEvent.setup();
    open([]);

    await create(user, "bench");
    await screen.findByText(SECRET);
    await user.click(screen.getByRole("button", { name: "Done" }));

    expect(screen.queryByText(SECRET)).not.toBeInTheDocument();
    expect(document.body.textContent).not.toContain(SECRET);
    expect(screen.queryByRole("heading", { name: "Token created" })).not.toBeInTheDocument();
  });

  it("removes the secret when the person leaves the tab, and does not bring it back", async () => {
    const user = userEvent.setup();
    open([]);

    await create(user, "bench");
    await screen.findByText(SECRET);

    await user.click(screen.getByRole("tab", { name: "Config" }));
    await screen.findByRole("heading", { name: "File settings" });
    expect(document.body.textContent).not.toContain(SECRET);

    await user.click(screen.getByRole("tab", { name: "API tokens" }));
    await screen.findByRole("region", { name: "API tokens" });
    expect(document.body.textContent).not.toContain(SECRET);
    expect(screen.queryByRole("heading", { name: "Token created" })).not.toBeInTheDocument();
  });

  it("replaces the secret when another token is created, so only the newest secret is on the page", async () => {
    const user = userEvent.setup();
    const other = `skt_${"Zx7Lm".repeat(8)}`;
    const second = () => json(201, { token: token("t2", { name: "second" }), secret: other });
    open([], { creates: [(body) => json(201, { token: token("t1", { name: String(body["name"]) }), secret: SECRET }), second] });

    await create(user, "first");
    await screen.findByText(SECRET);

    await create(user, "second");

    expect(await screen.findByText(other)).toBeInTheDocument();
    expect(document.body.textContent).not.toContain(SECRET);
    expect(screen.getAllByRole("heading", { name: "Token created" })).toHaveLength(1);
  });

  it("drops the secret of the first token as the second create starts, even when the second fails", async () => {
    const user = userEvent.setup();
    const first = (body: Record<string, unknown>) => json(201, { token: token("t1", { name: String(body["name"]), expiresAt: String(body["expiresAt"]) }), secret: SECRET });
    const second = () => problem(500, "The database is down.");
    open([], { creates: [first, second] });

    await create(user, "first");
    await screen.findByText(SECRET);

    await create(user, "second");

    expect(await screen.findByText("The database is down.")).toBeInTheDocument();
    expect(document.body.textContent).not.toContain(SECRET);
    expect(screen.queryByRole("heading", { name: "Token created" })).not.toBeInTheDocument();
  });

  it("keeps the secret out of localStorage, sessionStorage, the query cache and the mutation cache", async () => {
    const user = userEvent.setup();
    const queryClient = createQueryClient();
    open([], { queryClient });

    await create(user, "bench");
    await screen.findByText(SECRET);
    await screen.findByRole("rowheader", { name: "bench" });

    const queries = JSON.stringify(queryClient.getQueryCache().getAll().map((q) => q.state));
    const mutations = queryClient.getMutationCache().getAll();
    const mutationState = JSON.stringify(mutations.map((m) => ({ state: m.state, options: m.options.mutationKey })));

    expect(queries).toContain("bench");
    expect(mutations.length).toBeGreaterThan(0);
    expect(mutationState).toContain("bench");
    expect(queries).not.toContain(SECRET);
    expect(mutationState).not.toContain(SECRET);
    expect(JSON.stringify({ ...localStorage })).not.toContain(SECRET);
    expect(JSON.stringify({ ...sessionStorage })).not.toContain(SECRET);
    expect(Object.keys(localStorage).concat(Object.keys(sessionStorage)).some((key) => key.includes(SECRET))).toBe(false);
  });

  it("shows a refusal with a field under that field", async () => {
    const user = userEvent.setup();
    const refuse = () => validationProblem([{ field: "name", message: "The name is already used." }]);
    open([], { creates: [refuse] });

    await create(user, "bench");

    const name = await screen.findByRole("textbox", { name: "Name" });
    await vi.waitFor(() => expect(name).toHaveAccessibleDescription(expect.stringContaining("The name is already used.")));
    expect(name).toHaveAttribute("aria-invalid", "true");
    expect(name).toHaveValue("bench");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.queryByText(SECRET)).not.toBeInTheDocument();
  });

  it("shows a refusal of the expiry under the date field", async () => {
    const user = userEvent.setup();
    const refuse = () => validationProblem([{ field: "expiresAt", message: "The expiry must be in the future." }]);
    open([], { creates: [refuse] });

    await user.selectOptions(await screen.findByRole("combobox", { name: "Expires" }), "date");
    fireEvent.change(screen.getByLabelText("Last day the token works"), { target: { value: "2026-12-01" } });
    await create(user, "bench");

    expect(await screen.findByLabelText("Last day the token works")).toHaveAccessibleDescription(expect.stringContaining("The expiry must be in the future."));
  });

  it.each([
    { name: "a 400 with only a detail", answer: () => problem(400, "The expiry must be in the future and at most a year away."), text: "The expiry must be in the future and at most a year away." },
    { name: "a 500", answer: () => problem(500, "Something went wrong."), text: "Something went wrong." },
    { name: "a 422 with a problem about the whole body", answer: () => validationProblem([{ field: "", message: "The body is not valid." }]), text: "The body is not valid." },
  ])("shows $name in an alert at the top of the form", async ({ answer, text }) => {
    const user = userEvent.setup();
    open([], { creates: [answer] });

    await create(user, "bench");

    const alert = await screen.findByRole("alert");
    expect(within(alert).getByRole("heading", { name: "Could not create the token" })).toBeInTheDocument();
    expect(within(alert).getByText(text)).toBeInTheDocument();
    expect(alert).toHaveFocus();
  });

  it("refuses an empty name and a name of 101 characters without sending anything", async () => {
    const user = userEvent.setup();
    const { requests } = open([]);

    await user.click(await screen.findByRole("button", { name: "Create token" }));
    expect(await screen.findByText("Enter a name.")).toBeInTheDocument();

    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), { target: { value: "x".repeat(101) } });
    await user.click(screen.getByRole("button", { name: "Create token" }));
    expect(await screen.findByText("The name can have at most 100 characters.")).toBeInTheDocument();

    expect(posts(requests)).toHaveLength(0);
  });

  it("refuses a date more than a year away before sending, and accepts the last day", async () => {
    const user = userEvent.setup();
    const { requests } = open([]);

    await user.selectOptions(await screen.findByRole("combobox", { name: "Expires" }), "date");
    const date = screen.getByLabelText("Last day the token works");
    expect(date).toHaveAttribute("min", "2026-11-03");
    expect(date).toHaveAttribute("max", "2027-11-03");

    await fillName(user, "bench");
    fireEvent.change(date, { target: { value: "2027-11-04" } });
    await user.click(screen.getByRole("button", { name: "Create token" }));

    expect(await screen.findByText(/Pick a day on or before 2027-11-03\. A token lasts at most a year\./)).toBeInTheDocument();
    expect(posts(requests)).toHaveLength(0);
    expect(screen.queryByText(SECRET)).not.toBeInTheDocument();

    fireEvent.change(date, { target: { value: "2027-11-03" } });
    await user.click(screen.getByRole("button", { name: "Create token" }));

    expect(await screen.findByText(SECRET)).toBeInTheDocument();
    expect(posts(requests)).toHaveLength(1);
    expect(posts(requests)[0]?.body).toMatchObject({ expiresAt: new Date(2027, 10, 3, 23, 59, 59).toISOString() });
  });

  it("refuses a date in the past and a date left empty", async () => {
    const user = userEvent.setup();
    const { requests } = open([]);

    await user.selectOptions(await screen.findByRole("combobox", { name: "Expires" }), "date");
    await fillName(user, "bench");

    await user.click(screen.getByRole("button", { name: "Create token" }));
    expect(await screen.findByText("Pick the last day the token works.")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Last day the token works"), { target: { value: "2026-11-02" } });
    await user.click(screen.getByRole("button", { name: "Create token" }));
    expect(await screen.findByText("Pick today or a later day.")).toBeInTheDocument();

    expect(posts(requests)).toHaveLength(0);
  });
});

describe("the API tokens tab: revoking a token", () => {
  it("asks first, names the token, and sends nothing until the confirm button is pressed", async () => {
    const user = userEvent.setup();
    const { requests } = open([NIGHTLY]);

    await user.click(await screen.findByRole("button", { name: "Revoke token nightly bench" }));

    const group = screen.getByRole("group", { name: "Revoke this token" });
    expect(within(group).getByText("Revoke this token?")).toBeInTheDocument();
    expect(within(group).getByText("nightly bench")).toBeInTheDocument();
    expect(within(group).getByText("Anything that uses it stops working at once.")).toBeInTheDocument();
    expect(within(group).getByRole("button", { name: "Cancel" })).toHaveFocus();
    expect(requests.filter((r) => r.method === "DELETE")).toHaveLength(0);
  });

  it("sends nothing on Cancel and puts focus back on the Revoke button", async () => {
    const user = userEvent.setup();
    const { requests } = open([NIGHTLY]);

    await user.click(await screen.findByRole("button", { name: "Revoke token nightly bench" }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(screen.queryByRole("group", { name: "Revoke this token" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Revoke token nightly bench" })).toHaveFocus();
    expect(requests.filter((r) => r.method === "DELETE")).toHaveLength(0);
  });

  it("sends DELETE with the CSRF header on confirm, reads the list again, and the button is gone", async () => {
    const user = userEvent.setup();
    const { requests } = open([NIGHTLY, DEPLOY]);

    await user.click(await screen.findByRole("button", { name: "Revoke token nightly bench" }));
    await user.click(screen.getByRole("button", { name: "Revoke token" }));

    expect(await screen.findByRole("heading", { name: "Token revoked" })).toBeInTheDocument();
    expect(screen.getByText("nightly bench no longer works.")).toBeInTheDocument();

    const deletes = requests.filter((r) => r.method === "DELETE");
    expect(deletes).toHaveLength(1);
    expect(deletes[0]?.path).toBe(`${TOKENS_PATH}/${NIGHTLY.id}`);
    expect(deletes[0]?.csrf).toBe("csrf-ada");

    await vi.waitFor(() => expect(screen.queryByRole("button", { name: "Revoke token nightly bench" })).not.toBeInTheDocument());
    expect(screen.queryByRole("group", { name: "Revoke this token" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Revoke token deploy hook" })).toBeInTheDocument();

    const nightlyRow = tableRows().find((row) => within(row).queryByRole("rowheader", { name: "nightly bench" }));
    expect(within(nightlyRow as HTMLElement).getByText("Revoked")).toBeInTheDocument();
  });

  it("says so when the revoke fails, such as for a token that was already revoked", async () => {
    const user = userEvent.setup();
    open([NIGHTLY], { revoke: () => problem(404, "No API token has this ID, or it is already revoked.") });

    await user.click(await screen.findByRole("button", { name: "Revoke token nightly bench" }));
    await user.click(screen.getByRole("button", { name: "Revoke token" }));

    const alert = await screen.findByRole("alert");
    expect(within(alert).getByText("No API token has this ID, or it is already revoked.")).toBeInTheDocument();
    expect(within(alert).getByRole("heading", { name: "Could not revoke the token" })).toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Revoke this token" })).not.toBeInTheDocument();
  });
});
