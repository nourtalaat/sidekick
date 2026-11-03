import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { VIEWER, json, renderApp } from "../test/helpers";
import { ADMIN, RESPONDER, codedProblem, recording, route, sampleRoutes, validationProblem } from "../test/rules";

describe("the route form", () => {
  it("adds a route with the fields that were filled in, then shows the list with a message", async () => {
    const user = userEvent.setup();
    let saved = false;

    const { handler, seen } = recording((request) => {
      if (request.method === "POST") {
        saved = true;

        return json(201, route({ name: "search", version: 1 }));
      }

      return json(200, { routes: saved ? [route({ name: "search", version: 1 })] : sampleRoutes() });
    });

    renderApp({ me: ADMIN, route: "/routes/new", handler });

    expect(await screen.findByLabelText("Priority")).toHaveValue("100");

    await user.type(screen.getByLabelText("Name"), "search");
    await user.clear(screen.getByLabelText("Priority"));
    await user.type(screen.getByLabelText("Priority"), "10");
    await user.type(screen.getByLabelText("Service"), "search-*");
    await user.type(screen.getByLabelText("Severity"), "warning, critical");
    await user.click(screen.getByLabelText("Labels"));
    await user.paste("team=core\nenv=prod");
    await user.type(screen.getByLabelText("Outputs"), "slack-thread");
    await user.type(screen.getByLabelText("Most tool calls"), "20");
    await user.type(screen.getByLabelText("Longest run"), "5m");
    await user.type(screen.getByLabelText("Most cost in dollars"), "2.50");
    await user.click(screen.getByRole("button", { name: "Add route" }));

    expect(await screen.findByText("Route search saved. Every replica uses it within seconds.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Routes", level: 1 })).toBeInTheDocument();

    const post = seen.find((r) => r.method === "POST");
    expect(post?.path).toBe("/api/v1/routes");
    expect(post?.csrf).toBe(VIEWER.csrfToken);
    expect(post?.body).toEqual({
      name: "search",
      priority: 10,
      match: { service: "search-*", severity: ["warning", "critical"], labels: { team: "core", env: "prod" } },
      outputs: ["slack-thread"],
      mode: "read-only",
      budgets: { maxToolCalls: 20, maxWallTime: "5m", maxCostUSD: 2.5 },
      grouping: {},
    });
  });

  it("sends the details, one key=value to a line, and keeps an equals sign in a value", async () => {
    const user = userEvent.setup();

    const { handler, seen } = recording((request) =>
      request.method === "POST" ? json(201, route({ name: "search", version: 1 })) : json(200, { routes: sampleRoutes() }),
    );

    renderApp({ me: ADMIN, route: "/routes/new", handler });

    await user.type(await screen.findByLabelText("Name"), "search");
    await user.type(screen.getByLabelText("Service"), "search-*");
    await user.click(screen.getByLabelText("Details"));
    await user.paste("owner=payments team\nenvironment = prod\nnote=a=b");
    await user.click(screen.getByRole("button", { name: "Add route" }));

    expect(await screen.findByText("Route search saved. Every replica uses it within seconds.")).toBeInTheDocument();
    expect(seen.find((r) => r.method === "POST")?.body).toMatchObject({ details: { owner: "payments team", environment: "prod", note: "a=b" } });
  });

  it("sends no details when the box is empty", async () => {
    const user = userEvent.setup();
    const { handler, seen } = recording((request) => (request.method === "POST" ? json(201, route({ name: "search", version: 1 })) : json(200, { routes: sampleRoutes() })));
    renderApp({ me: ADMIN, route: "/routes/new", handler });

    await user.type(await screen.findByLabelText("Name"), "search");
    await user.type(screen.getByLabelText("Service"), "search-*");
    await user.click(screen.getByRole("button", { name: "Add route" }));

    expect(await screen.findByText("Route search saved. Every replica uses it within seconds.")).toBeInTheDocument();
    expect(seen.find((r) => r.method === "POST")?.body).not.toHaveProperty("details");
  });

  it("shows the details of a saved route, sorted, and sends the edited ones", async () => {
    const user = userEvent.setup();

    const { handler, seen } = recording((request) =>
      request.method === "PUT"
        ? json(200, route({ version: 2 }))
        : json(200, { routes: [route({ details: { owner: "payments team", environment: "prod" } })] }),
    );

    renderApp({ me: ADMIN, route: "/routes/checkout", handler });

    const details = await screen.findByLabelText("Details");
    expect(details).toHaveValue("environment=prod\nowner=payments team");

    await user.clear(details);
    await user.click(details);
    await user.paste("owner=checkout team");
    await user.click(screen.getByRole("button", { name: "Save route" }));

    expect(await screen.findByText("Route checkout saved. Every replica uses it within seconds.")).toBeInTheDocument();
    expect(seen.find((r) => r.method === "PUT")?.body).toMatchObject({ details: { owner: "checkout team" } });
  });

  it("removes the details of a saved route when the box is cleared", async () => {
    const user = userEvent.setup();

    const { handler, seen } = recording((request) =>
      request.method === "PUT" ? json(200, route({ version: 2 })) : json(200, { routes: [route({ details: { owner: "payments team" } })] }),
    );

    renderApp({ me: ADMIN, route: "/routes/checkout", handler });

    await user.clear(await screen.findByLabelText("Details"));
    await user.click(screen.getByRole("button", { name: "Save route" }));

    expect(await screen.findByText("Route checkout saved. Every replica uses it within seconds.")).toBeInTheDocument();
    expect(seen.find((r) => r.method === "PUT")?.body).not.toHaveProperty("details");
  });

  it("asks for details written as key=value, and shows the problems the server finds under the field", async () => {
    const user = userEvent.setup();

    const { handler, seen } = recording((request) =>
      request.method === "POST"
        ? validationProblem([{ field: "details", message: 'key "on call" must be 1 to 64 characters from letters, digits, "-", "_" and "."' }])
        : json(200, { routes: [] }),
    );

    renderApp({ me: ADMIN, route: "/routes/new", handler });

    await user.type(await screen.findByLabelText("Name"), "search");
    await user.click(screen.getByLabelText("Details"));
    await user.paste("owner");
    await user.click(screen.getByRole("button", { name: "Add route" }));

    expect(screen.getByLabelText("Details")).toHaveAccessibleDescription(expect.stringContaining('"owner" is not written as name=value.'));
    expect(seen.filter((r) => r.method === "POST")).toHaveLength(0);

    await user.clear(screen.getByLabelText("Details"));
    await user.paste("on call=x");
    await user.click(screen.getByRole("button", { name: "Add route" }));

    expect(await screen.findByLabelText("Details")).toHaveAccessibleDescription(expect.stringContaining('key "on call" must be 1 to 64 characters'));
    expect(seen.filter((r) => r.method === "POST")).toHaveLength(1);
  });

  it("offers read-only, approved and read + write as the modes", async () => {
    renderApp({ me: ADMIN, route: "/routes/new" });

    const mode = await screen.findByLabelText("Mode");

    expect(within(mode).getAllByRole("option")).toHaveLength(3);
    expect(within(mode).getByRole("option", { name: "Read-only" })).toBeInTheDocument();
    expect(within(mode).getByRole("option", { name: /^Approved/ })).toBeInTheDocument();
    expect(within(mode).getByRole("option", { name: /^Read \+ write/ })).toBeInTheDocument();
  });

  it("offers the override for senders that are not trusted only in read + write mode", async () => {
    const user = userEvent.setup();
    renderApp({ me: ADMIN, route: "/routes/new" });

    await screen.findByLabelText("Mode");
    expect(screen.queryByLabelText("Also allow senders that are not trusted")).not.toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText("Mode"), "approved");
    expect(screen.queryByLabelText("Also allow senders that are not trusted")).not.toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText("Mode"), "read-write");
    expect(screen.getByLabelText("Also allow senders that are not trusted")).not.toBeChecked();
  });

  it("saves a read + write route without the override at once", async () => {
    const user = userEvent.setup();
    const { handler, seen } = recording((request) => (request.method === "POST" ? json(201, route({ name: "search", version: 1 })) : json(200, { routes: sampleRoutes() })));
    renderApp({ me: ADMIN, route: "/routes/new", handler });

    await user.type(await screen.findByLabelText("Name"), "search");
    await user.type(screen.getByLabelText("Intake"), "alertmanager");
    await user.selectOptions(screen.getByLabelText("Mode"), "read-write");
    await user.type(screen.getByLabelText("Actions"), "monitor-note");
    await user.click(screen.getByRole("button", { name: "Add route" }));

    expect(await screen.findByText("Route search saved. Every replica uses it within seconds.")).toBeInTheDocument();

    const body = seen.find((r) => r.method === "POST")?.body;
    expect(body).toMatchObject({ mode: "read-write", actions: ["monitor-note"] });
    expect(body).not.toHaveProperty("allowUntrustedWrite");
  });

  it("asks the admin to confirm the override before it sends anything", async () => {
    const user = userEvent.setup();
    const { handler, seen } = recording((request) => (request.method === "POST" ? json(201, route({ name: "search", version: 1 })) : json(200, { routes: sampleRoutes() })));
    renderApp({ me: ADMIN, route: "/routes/new", handler });

    await user.type(await screen.findByLabelText("Name"), "search");
    await user.type(screen.getByLabelText("Service"), "search-*");
    await user.selectOptions(screen.getByLabelText("Mode"), "read-write");
    await user.type(screen.getByLabelText("Actions"), "monitor-note");
    await user.click(screen.getByLabelText("Also allow senders that are not trusted"));
    await user.click(screen.getByRole("button", { name: "Add route" }));

    const confirm = screen.getByRole("group", { name: "Confirm writes for senders that are not trusted" });
    expect(within(confirm).getByText(/Route search will approve and run/)).toBeInTheDocument();
    expect(within(confirm).getByRole("button", { name: "Cancel" })).toHaveFocus();
    expect(seen.filter((r) => r.method === "POST")).toHaveLength(0);

    await user.click(within(confirm).getByRole("button", { name: "Cancel" }));

    expect(screen.queryByRole("group", { name: "Confirm writes for senders that are not trusted" })).not.toBeInTheDocument();
    expect(seen.filter((r) => r.method === "POST")).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "Add route" }));
    await user.click(screen.getByRole("button", { name: "Allow untrusted writes" }));

    expect(await screen.findByText("Route search saved. Every replica uses it within seconds.")).toBeInTheDocument();

    const posts = seen.filter((r) => r.method === "POST");
    expect(posts).toHaveLength(1);
    expect(posts[0]?.body).toMatchObject({ mode: "read-write", allowUntrustedWrite: true, actions: ["monitor-note"] });
  });

  it("does not ask while the form has problems of its own", async () => {
    const user = userEvent.setup();
    const { handler, seen } = recording(() => json(200, { routes: [] }));
    renderApp({ me: ADMIN, route: "/routes/new", handler });

    await user.type(await screen.findByLabelText("Name"), "search");
    await user.selectOptions(screen.getByLabelText("Mode"), "read-write");
    await user.click(screen.getByLabelText("Also allow senders that are not trusted"));
    await user.type(screen.getByLabelText("Most tokens"), "lots");
    await user.click(screen.getByRole("button", { name: "Add route" }));

    expect(screen.queryByRole("group", { name: "Confirm writes for senders that are not trusted" })).not.toBeInTheDocument();
    expect(seen.filter((r) => r.method === "POST")).toHaveLength(0);
  });

  it("shows the override of a saved route and keeps it on save after the confirmation", async () => {
    const user = userEvent.setup();

    const { handler, seen } = recording((request) =>
      request.method === "PUT"
        ? json(200, route({ version: 2, mode: "read-write", allowUntrustedWrite: true }))
        : json(200, { routes: [route({ mode: "read-write", actions: ["monitor-note"], allowUntrustedWrite: true })] }),
    );

    renderApp({ me: ADMIN, route: "/routes/checkout", handler });

    expect(await screen.findByLabelText("Mode")).toHaveValue("read-write");
    expect(screen.getByLabelText("Also allow senders that are not trusted")).toBeChecked();

    await user.click(screen.getByRole("button", { name: "Save route" }));
    await user.click(screen.getByRole("button", { name: "Allow untrusted writes" }));

    expect(await screen.findByText("Route checkout saved. Every replica uses it within seconds.")).toBeInTheDocument();
    expect(seen.find((r) => r.method === "PUT")?.body).toMatchObject({ mode: "read-write", allowUntrustedWrite: true, actions: ["monitor-note"] });
  });

  it("sends the mode and the actions of an approved route", async () => {
    const user = userEvent.setup();

    const { handler, seen } = recording((request) =>
      request.method === "POST" ? json(201, route({ name: "search", version: 1 })) : json(200, { routes: sampleRoutes() }),
    );

    renderApp({ me: ADMIN, route: "/routes/new", handler });

    await user.type(await screen.findByLabelText("Name"), "search");
    await user.type(screen.getByLabelText("Service"), "search-*");
    await user.selectOptions(screen.getByLabelText("Mode"), "approved");
    await user.type(screen.getByLabelText("Actions"), "monitor-pr, ticket-comment");
    await user.click(screen.getByRole("button", { name: "Add route" }));

    expect(await screen.findByText("Route search saved. Every replica uses it within seconds.")).toBeInTheDocument();
    expect(seen.find((r) => r.method === "POST")?.body).toMatchObject({ mode: "approved", actions: ["monitor-pr", "ticket-comment"] });
  });

  it("asks for numbers and labels the way the form needs them, without sending anything", async () => {
    const user = userEvent.setup();
    const { handler, seen } = recording(() => json(200, { routes: [] }));
    renderApp({ me: ADMIN, route: "/routes/new", handler });

    await user.type(await screen.findByLabelText("Name"), "search");
    await user.clear(screen.getByLabelText("Priority"));
    await user.type(screen.getByLabelText("Priority"), "soon");
    await user.type(screen.getByLabelText("Most tokens"), "lots");
    await user.type(screen.getByLabelText("Most cost in dollars"), "two");
    await user.click(screen.getByLabelText("Labels"));
    await user.paste("team");
    await user.click(screen.getByRole("button", { name: "Add route" }));

    expect(screen.getByLabelText("Priority")).toHaveAccessibleDescription(expect.stringContaining("Enter a whole number."));
    expect(screen.getByLabelText("Most tokens")).toHaveAccessibleDescription(expect.stringContaining("Enter a whole number."));
    expect(screen.getByLabelText("Most cost in dollars")).toHaveAccessibleDescription(expect.stringContaining("Enter an amount such as 2.50."));
    expect(screen.getByLabelText("Labels")).toHaveAccessibleDescription(expect.stringContaining('"team" is not written as name=value.'));
    expect(screen.getByRole("alert")).toHaveFocus();
    expect(seen.filter((r) => r.method === "POST")).toHaveLength(0);
  });

  it("shows each problem the server found under its field, a list item under its list, and the others at the top", async () => {
    const user = userEvent.setup();

    const { handler } = recording((request) =>
      request.method === "POST"
        ? validationProblem([
            { field: "budgets.maxToolCalls", message: "100 is above the global ceiling budgets.perInvestigation.maxToolCalls 40" },
            { field: "sources[1]", message: 'source "gone" is not defined' },
            { field: "match", message: "required: a runtime route needs a match" },
            { field: "name", message: 'route name "Search" must start with a lower-case letter' },
            { field: "roles", message: "is not a field of the form" },
          ])
        : json(200, { routes: [] }),
    );

    renderApp({ me: ADMIN, route: "/routes/new", handler });

    await user.type(await screen.findByLabelText("Name"), "Search");
    await user.click(screen.getByRole("button", { name: "Add route" }));

    expect(await screen.findByLabelText("Most tool calls")).toHaveAccessibleDescription(expect.stringContaining("100 is above the global ceiling"));
    expect(screen.getByLabelText("Sources")).toHaveAccessibleDescription(expect.stringContaining('source "gone" is not defined'));
    expect(screen.getByLabelText("Name")).toHaveAccessibleDescription(expect.stringContaining("must start with a lower-case letter"));
    expect(screen.getByText("required: a runtime route needs a match")).toBeInTheDocument();

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("roles: is not a field of the form");
    expect(alert).toHaveFocus();
  });

  it("shows the refusal of an approval-only action in read + write mode under the actions", async () => {
    const user = userEvent.setup();
    const message =
      'action "rollback" (type gated.note) is approval-only, so a read-write route may not list it as it is; use mode: approved on this route, or set allowReadWrite: true on the action in the config file';

    const { handler } = recording((request) =>
      request.method === "POST" ? validationProblem([{ field: "actions[0]", message }]) : json(200, { routes: [] }),
    );

    renderApp({ me: ADMIN, route: "/routes/new", handler });

    await user.type(await screen.findByLabelText("Name"), "ops");
    await user.selectOptions(screen.getByLabelText("Mode"), "read-write");
    await user.click(screen.getByLabelText("Actions"));
    await user.paste("rollback");
    await user.click(screen.getByRole("button", { name: "Add route" }));

    expect(await screen.findByLabelText("Actions")).toHaveAccessibleDescription(expect.stringContaining(message));
  });

  it("changes a runtime route, and sends back the version it read", async () => {
    const user = userEvent.setup();

    const { handler, seen } = recording((request) =>
      request.method === "PUT" ? json(200, route({ priority: 5, version: 2 })) : json(200, { routes: sampleRoutes().map((r) => (r.name === "checkout" ? route({ enabled: false, status: "disabled" }) : r)) }),
    );

    renderApp({ me: ADMIN, route: "/routes/checkout", handler });

    const priority = await screen.findByLabelText("Priority");
    expect(priority).toHaveValue("20");
    expect(screen.getByLabelText("Name")).toBeDisabled();
    expect(screen.getByLabelText("Service")).toHaveValue("checkout-*");
    expect(screen.getByLabelText("Outputs")).toHaveValue("slack-thread");
    expect(screen.getByLabelText("In use")).not.toBeChecked();

    await user.clear(priority);
    await user.type(priority, "5");
    await user.click(screen.getByLabelText("In use"));
    await user.click(screen.getByRole("button", { name: "Save route" }));

    expect(await screen.findByText("Route checkout saved. Every replica uses it within seconds.")).toBeInTheDocument();

    const put = seen.find((r) => r.method === "PUT");
    expect(put?.path).toBe("/api/v1/routes/checkout");
    expect(put?.body).toMatchObject({ priority: 5, enabled: true, version: 1, mode: "read-only", match: { service: "checkout-*" }, outputs: ["slack-thread"] });
  });

  it("warns that someone changed the route", async () => {
    const user = userEvent.setup();

    const { handler } = recording((request) =>
      request.method === "PUT" ? codedProblem(409, "stale", "The route checkout was changed since you read it.") : json(200, { routes: [route()] }),
    );

    renderApp({ me: ADMIN, route: "/routes/checkout", handler });

    await user.click(await screen.findByRole("button", { name: "Save route" }));

    expect(await screen.findByRole("heading", { name: "Someone changed this since you opened it" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Load the new version" })).toBeInTheDocument();
  });

  it("tells that a route of the config file cannot be changed", async () => {
    const { handler } = recording(() => json(200, { routes: sampleRoutes().slice(0, 1) }));
    renderApp({ me: ADMIN, route: "/routes/payments", handler });

    expect(await screen.findByRole("heading", { name: "No route to change" })).toBeInTheDocument();
  });

  it("is for an admin", async () => {
    renderApp({ me: RESPONDER, route: "/routes/new" });

    expect(await screen.findByRole("heading", { name: "Needs the admin role" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add route" })).not.toBeInTheDocument();
  });
});
