import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { RequireSession } from "../../session";
import { VIEWER, fakeServer, json, problem, renderApp, renderWithProviders, stubMedia, wcagViolations, type Handler } from "../../test/helpers";
import { APPROVER, proposal } from "../../test/actionProposals";
import { recording, type Recorded } from "../../test/rules";
import { ActionsTab } from "./ActionsTab";

const NAMES = ["datadog-monitor", "monitor-note", "slack-react"];

const CHANGE_FILTERS = "Change the action or the status to see other proposals.";

/** A handler that serves the names of the actions and the proposals for the statuses asked. */
function queue() {
  return recording((request) => {
    if (request.path === "/api/v1/proposals/actions") {
      return json(200, { actions: NAMES });
    }

    if (request.path === "/api/v1/proposals") {
      const ran = request.search.getAll("status").includes("succeeded");

      return json(200, {
        proposals: ran
          ? [proposal({ id: "0192d3a4-0000-7000-8000-0000000000f4", action: "ticket-comment", status: "succeeded", decidedByName: "Ana" })]
          : [proposal(), proposal({ id: "0192d3a4-0000-7000-8000-0000000000f2", action: "slack-react" })],
      });
    }

    return json(404, {});
  });
}

/** A handler that serves the names of the actions and no proposal. */
function emptyQueue() {
  return recording((request) => (request.path === "/api/v1/proposals/actions" ? json(200, { actions: NAMES }) : json(200, { proposals: [] })));
}

/** The requests for proposals, oldest first. */
function listRequests(seen: Recorded[]): Recorded[] {
  return seen.filter((r) => r.path === "/api/v1/proposals");
}

function Address() {
  return <output aria-label="address">{useLocation().search}</output>;
}

/** Renders the tab alone at route, next to a line that shows the address. */
function renderTab(route: string, handler: Handler) {
  const server = fakeServer(APPROVER, handler);

  return renderWithProviders(
    <Routes>
      <Route element={<RequireSession />}>
        <Route
          path="/review"
          element={
            <>
              <ActionsTab />
              <Address />
            </>
          }
        />
      </Route>
    </Routes>,
    { api: server.api, route },
  );
}

describe("the actions tab of the review queue", () => {
  it("lists the actions that wait for a decision on the actions tab", async () => {
    const { handler, seen } = queue();
    renderApp({ me: APPROVER, route: "/review?tab=actions", handler });

    expect(await screen.findByRole("heading", { name: "Review queue", level: 1 })).toBeInTheDocument();
    expect(await screen.findByRole("article", { name: "Proposed action monitor-note" })).toBeInTheDocument();
    expect(screen.getByRole("article", { name: "Proposed action slack-react" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Actions" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByLabelText("Status")).toHaveValue("waiting");
    expect(screen.getByLabelText("Action")).toHaveValue("");

    const request = listRequests(seen)[0];
    expect(request?.search.getAll("status")).toEqual(["proposed"]);
    expect(request?.search.has("action")).toBe(false);
  });

  it("offers any action and the names of the configured actions", async () => {
    const { handler } = queue();
    renderApp({ me: APPROVER, route: "/review?tab=actions", handler });

    const menu = await screen.findByLabelText("Action");
    await screen.findByRole("option", { name: "slack-react" });

    expect(within(menu).getAllByRole("option").map((o) => o.textContent)).toEqual(["Any action", ...NAMES]);
  });

  it.each([
    ["waiting", "Waiting for a decision", ["proposed"]],
    ["approved", "Approved", ["approved", "executing"]],
    ["ran", "Ran", ["succeeded"]],
    ["failed", "Failed", ["failed"]],
    ["expired", "Expired", ["expired"]],
    ["undone", "Undone", ["undone"]],
    ["rejected", "Rejected", ["rejected"]],
    ["any", "Any status", ["proposed", "approved", "executing", "succeeded", "failed", "expired", "undone", "rejected"]],
  ])("asks for the statuses of %s when it is chosen in the menu", async (word, label, statuses) => {
    const user = userEvent.setup();
    const { handler, seen } = queue();
    const start = word === "expired" ? "failed" : "expired";
    renderApp({ me: APPROVER, route: `/review?tab=actions&status=${start}`, handler });

    const menu = await screen.findByLabelText("Status");
    expect(within(menu).getByRole("option", { name: label })).toHaveValue(word);

    await user.selectOptions(menu, word);

    await waitFor(() => expect(listRequests(seen).at(-1)?.search.getAll("status")).toEqual(statuses));
  });

  it("shows the ran actions without buttons to decide", async () => {
    const user = userEvent.setup();
    const { handler } = queue();
    renderApp({ me: APPROVER, route: "/review?tab=actions", handler });

    await user.selectOptions(await screen.findByLabelText("Status"), "ran");

    const card = await screen.findByRole("article", { name: "Proposed action ticket-comment" });
    expect(within(card).getByText("Done")).toBeInTheDocument();
    expect(within(card).queryByRole("button")).not.toBeInTheDocument();
  });

  it("asks only for the chosen action, with the statuses and the cursor", async () => {
    const user = userEvent.setup();
    const { handler, seen } = recording((request) =>
      request.path === "/api/v1/proposals/actions"
        ? json(200, { actions: NAMES })
        : json(200, request.search.get("cursor") ? { proposals: [] } : { proposals: [proposal()], nextCursor: "abc" }),
    );
    renderApp({ me: APPROVER, route: "/review?tab=actions", handler });

    await user.selectOptions(await screen.findByLabelText("Action"), "slack-react");
    await user.click(await screen.findByRole("button", { name: "Show more" }));

    await waitFor(() => expect(listRequests(seen).at(-1)?.search.get("cursor")).toBe("abc"));

    const last = listRequests(seen).at(-1);
    expect(last?.search.get("action")).toBe("slack-react");
    expect(last?.search.getAll("status")).toEqual(["proposed"]);
  });

  it("writes both choices to the address and drops them when they are the defaults", async () => {
    const user = userEvent.setup();
    const { handler } = queue();
    renderTab("/review?tab=actions", handler);

    const address = await screen.findByLabelText("address");
    expect(address).toHaveTextContent("?tab=actions");

    await user.selectOptions(await screen.findByLabelText("Status"), "failed");
    expect(address).toHaveTextContent("?tab=actions&status=failed");

    await user.selectOptions(screen.getByLabelText("Action"), "slack-react");
    expect(address).toHaveTextContent("?tab=actions&status=failed&action=slack-react");

    await user.selectOptions(screen.getByLabelText("Status"), "waiting");
    await user.selectOptions(screen.getByLabelText("Action"), "");
    expect(address).toHaveTextContent(/^\?tab=actions$/);
  });

  it("reads both choices from the address, and never asks for a list without the action", async () => {
    const { handler, seen } = queue();
    renderTab("/review?tab=actions&action=slack-react&status=ran", handler);

    expect(await screen.findByLabelText("Status")).toHaveValue("ran");
    await screen.findByRole("article", { name: /Proposed action/ });

    expect(screen.getByLabelText("Action")).toHaveValue("slack-react");

    const requests = listRequests(seen);
    expect(requests.length).toBeGreaterThan(0);

    for (const request of requests) {
      expect(request.search.get("action")).toBe("slack-react");
      expect(request.search.getAll("status")).toEqual(["succeeded"]);
    }
  });

  it("ignores a status or an action in the address that it does not know", async () => {
    const { handler, seen } = queue();
    renderTab("/review?tab=actions&action=deploy&status=maybe", handler);

    expect(await screen.findByLabelText("Status")).toHaveValue("waiting");
    await screen.findByRole("article", { name: /Proposed action/ });

    expect(screen.getByLabelText("Action")).toHaveValue("");

    const request = listRequests(seen)[0];
    expect(request?.search.getAll("status")).toEqual(["proposed"]);
    expect(request?.search.has("action")).toBe(false);
  });

  it("lists the proposals without the action filter when the names cannot be loaded", async () => {
    const { handler, seen } = recording((request) =>
      request.path === "/api/v1/proposals/actions" ? problem(404, "The config is not loaded.") : json(200, { proposals: [proposal()] }),
    );
    renderTab("/review?tab=actions&action=slack-react", handler);

    expect(await screen.findByText("The config is not loaded.")).toBeInTheDocument();
    expect(await screen.findByRole("article", { name: "Proposed action monitor-note" })).toBeInTheDocument();
    expect(listRequests(seen)[0]?.search.has("action")).toBe(false);
  });

  it.each([
    ["", "Nothing waits for a decision", "When the model proposes an action on a route in approved mode, it shows here."],
    ["&action=datadog-monitor", "Nothing waits for a decision for datadog-monitor", CHANGE_FILTERS],
    ["&status=failed&action=datadog-monitor", "No failed actions for datadog-monitor", CHANGE_FILTERS],
    ["&status=ran", "No actions ran", CHANGE_FILTERS],
    ["&status=approved", "No approved actions", CHANGE_FILTERS],
    ["&status=expired", "No expired actions", CHANGE_FILTERS],
    ["&status=undone", "No undone actions", CHANGE_FILTERS],
    ["&status=rejected&action=slack-react", "No rejected actions for slack-react", CHANGE_FILTERS],
    ["&status=any", "No action proposals yet", "Actions the model proposes show here."],
    ["&status=any&action=slack-react", "No action proposals for slack-react", CHANGE_FILTERS],
  ])("says what is missing when the address has %s and nothing is listed", async (filters, title, text) => {
    const { handler } = emptyQueue();
    renderApp({ me: APPROVER, route: `/review?tab=actions${filters}`, handler });

    expect(await screen.findByRole("heading", { name: title, level: 2 })).toBeInTheDocument();
    expect(screen.getByText(text)).toBeInTheDocument();
  });

  it("asks for more when there is another page", async () => {
    const user = userEvent.setup();
    const { handler, seen } = recording((request) =>
      request.path === "/api/v1/proposals/actions"
        ? json(200, { actions: NAMES })
        : json(200, request.search.get("cursor") ? { proposals: [proposal({ id: "0192d3a4-0000-7000-8000-0000000000f9", action: "slack-react" })] } : { proposals: [proposal()], nextCursor: "abc" }),
    );
    renderApp({ me: APPROVER, route: "/review?tab=actions", handler });

    await user.click(await screen.findByRole("button", { name: "Show more" }));

    expect(await screen.findByRole("article", { name: "Proposed action slack-react" })).toBeInTheDocument();
    expect(seen.at(-1)?.search.get("cursor")).toBe("abc");
  });

  it("tells someone below approver that the tab is for approvers, without asking for the queue or the names", async () => {
    const { handler, seen } = queue();
    renderApp({ me: VIEWER, route: "/review?tab=actions", handler });

    expect(await screen.findByText("Only approvers can see and decide the actions the model proposed.")).toBeInTheDocument();
    expect(seen.filter((r) => r.path.startsWith("/api/v1/proposals"))).toHaveLength(0);
  });
});

describe.each([false, true])("accessibility of the actions tab, dark %s", (dark) => {
  beforeEach(() => {
    stubMedia({ dark, forcedColors: false });
  });

  afterEach(() => {
    document.documentElement.removeAttribute("data-theme");
  });

  it("has no WCAG 2.2 AA violations with the filters", async () => {
    const { handler } = queue();
    const { container } = renderApp({ me: APPROVER, route: "/review?tab=actions&status=any&action=slack-react", handler });

    await screen.findByRole("article", { name: "Proposed action monitor-note" });

    expect(await wcagViolations(container)).toEqual([]);
  });

  it("has no WCAG 2.2 AA violations on an empty list that names the filters", async () => {
    const { handler } = emptyQueue();
    const { container } = renderApp({ me: APPROVER, route: "/review?tab=actions&status=failed&action=datadog-monitor", handler });

    await screen.findByRole("heading", { name: "No failed actions for datadog-monitor", level: 2 });

    expect(await wcagViolations(container)).toEqual([]);
  });
});
