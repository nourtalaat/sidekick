import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import { VIEWER, json, renderApp } from "../test/helpers";
import { ADMIN, RESPONDER, recording, rowAt } from "../test/rules";

type AuditEntry = components["schemas"]["AuditEntry"];

const INVESTIGATION = "01920000-0000-7000-8000-0000000000a1";

function entry(over: Partial<AuditEntry> = {}): AuditEntry {
  return {
    id: 7,
    at: "2026-10-20T10:05:00Z",
    actorKind: "user",
    actorId: "01920000-0000-7000-8000-000000000040",
    actorName: "Ad",
    action: "route.updated",
    targetKind: "route",
    targetId: "checkout",
    old: { priority: 20 },
    new: { priority: 5 },
    ...over,
  };
}

describe("the audit log page", () => {
  it("lists the entries with who, what, to what and the change on request", async () => {
    const user = userEvent.setup();

    const { handler } = recording(() =>
      json(200, {
        entries: [
          entry(),
          entry({ id: 6, actorKind: "policy", actorId: undefined, actorName: undefined, action: "action.executed", targetKind: "proposal", targetId: "p1", investigationId: INVESTIGATION, old: undefined, new: undefined }),
          entry({ id: 5, actorKind: "user", actorId: "U01SLACK", actorName: undefined, action: "listener.created", targetKind: "listener", targetId: "billing", old: undefined, new: { channel: "#billing" } }),
        ],
      }),
    );

    renderApp({ me: ADMIN, route: "/audit", handler });

    const table = await screen.findByRole("region", { name: "Audit log" });
    const rows = within(table).getAllByRole("row");

    expect(rows).toHaveLength(4);
    expect(within(rowAt(rows, 1)).getByText("Ad")).toBeInTheDocument();
    expect(within(rowAt(rows, 1)).getByText(/User, 01920000/)).toBeInTheDocument();
    expect(within(rowAt(rows, 1)).getByText("route.updated")).toBeInTheDocument();
    expect(within(rowAt(rows, 1)).getByText("checkout")).toBeInTheDocument();
    expect(rowAt(rows, 1).querySelector("time")).toHaveAttribute("datetime", "2026-10-20T10:05:00Z");

    expect(within(rowAt(rows, 2)).getByText("Policy")).toBeInTheDocument();
    expect(within(rowAt(rows, 2)).getByRole("link", { name: `Investigation ${INVESTIGATION}` })).toHaveAttribute("href", `/investigations/${INVESTIGATION}`);
    expect(within(rowAt(rows, 2)).queryByText(/Show change/)).not.toBeInTheDocument();

    expect(within(rowAt(rows, 3)).getByText("U01SLACK")).toBeInTheDocument();

    await user.click(within(rowAt(rows, 1)).getByText(/Show change/));
    expect(within(rowAt(rows, 1)).getByText(/"priority": 20/)).toBeInTheDocument();
    expect(within(rowAt(rows, 1)).getByText(/"priority": 5/)).toBeInTheDocument();
  });

  it("starts from the filters in the address and asks for them in UTC", async () => {
    const { handler, seen } = recording(() => json(200, { entries: [] }));
    renderApp({ me: ADMIN, route: "/audit?actor=policy&action=route.updated&from=2026-10-20T10:00&investigation=" + INVESTIGATION, handler });

    expect(await screen.findByRole("heading", { name: "No entries match these filters" })).toBeInTheDocument();

    expect(screen.getByLabelText("Who")).toHaveValue("policy");
    expect(screen.getByLabelText("Action")).toHaveValue("route.updated");
    expect(screen.getByLabelText("From")).toHaveValue("2026-10-20T10:00");
    expect(screen.getByLabelText("Investigation ID")).toHaveValue(INVESTIGATION);

    const request = seen.find((r) => r.path === "/api/v1/audit");
    expect(request?.search.get("actor")).toBe("policy");
    expect(request?.search.get("action")).toBe("route.updated");
    expect(request?.search.get("investigation")).toBe(INVESTIGATION);
    expect(request?.search.get("from")).toBe(new Date("2026-10-20T10:00").toISOString());
    expect(request?.search.has("to")).toBe(false);
    expect(request?.search.has("cursor")).toBe(false);
  });

  it("applies the filters to the request, and clears them", async () => {
    const user = userEvent.setup();
    const { handler, seen } = recording(() => json(200, { entries: [entry()] }));
    renderApp({ me: ADMIN, route: "/audit", handler });

    await user.type(await screen.findByLabelText("Who"), "U01SLACK");
    await user.type(screen.getByLabelText("Action"), "listener.created");
    await user.click(screen.getByRole("button", { name: "Filter" }));

    const lastRequest = () => seen.filter((r) => r.path === "/api/v1/audit").at(-1);

    await waitFor(() => expect(lastRequest()?.search.get("actor")).toBe("U01SLACK"));
    expect(lastRequest()?.search.get("action")).toBe("listener.created");

    await user.click(screen.getByRole("button", { name: "Clear filters" }));

    expect(screen.getByLabelText("Who")).toHaveValue("");
    await waitFor(() => expect(lastRequest()?.search.has("actor")).toBe(false));
  });

  it("shows older entries with the cursor of the page before", async () => {
    const user = userEvent.setup();

    const { handler, seen } = recording((request) =>
      request.search.get("cursor") === "next-page"
        ? json(200, { entries: [entry({ id: 1, action: "route.created" })] })
        : json(200, { entries: [entry({ id: 2 })], nextCursor: "next-page" }),
    );

    renderApp({ me: ADMIN, route: "/audit", handler });

    await user.click(await screen.findByRole("button", { name: "Show older entries" }));

    expect(await screen.findByText("route.created")).toBeInTheDocument();
    expect(screen.getAllByRole("row")).toHaveLength(3);
    expect(screen.queryByRole("button", { name: "Show older entries" })).not.toBeInTheDocument();
    expect(seen.at(-1)?.search.get("cursor")).toBe("next-page");
  });

  it("says when nothing is recorded yet", async () => {
    const { handler } = recording(() => json(200, { entries: [] }));
    renderApp({ me: ADMIN, route: "/audit", handler });

    expect(await screen.findByRole("heading", { name: "Nothing recorded yet" })).toBeInTheDocument();
  });

  it("is for an admin, and asks for nothing otherwise", async () => {
    const { handler, seen } = recording(() => json(200, { entries: [] }));
    renderApp({ me: RESPONDER, route: "/audit", handler });

    expect(await screen.findByRole("heading", { name: "Needs the admin role" })).toBeInTheDocument();
    expect(seen).toHaveLength(0);
  });

  it("is also closed to a viewer", async () => {
    renderApp({ me: VIEWER, route: "/audit" });

    expect(await screen.findByRole("heading", { name: "Needs the admin role" })).toBeInTheDocument();
  });
});
