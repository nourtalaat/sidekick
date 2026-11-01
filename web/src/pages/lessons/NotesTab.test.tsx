import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { ServiceNotes } from "../../api/notes";
import { VIEWER, json, renderApp, wcagViolations } from "../../test/helpers";
import { ADMIN, RESPONDER, recording, validationProblem } from "../../test/rules";

const LIST = "/api/v1/services/notes";

function summary(service: string, over: Record<string, unknown> = {}) {
  return { service, preview: `Notes about ${service}.`, updatedAt: "2026-10-20T10:00:00Z", updatedByName: "Ada Lovelace", ...over };
}

function notes(service: string, over: Partial<ServiceNotes> = {}): ServiceNotes {
  return { service, text: `Notes about ${service}.`, updatedAt: "2026-10-20T10:00:00Z", updatedByName: "Ada Lovelace", ...over };
}

/** A fake server with a list of services and the notes of each. */
function server(stored: Record<string, ServiceNotes>, onWrite?: (service: string, text: string) => void) {
  return recording((request) => {
    if (request.path === LIST) {
      return json(200, { services: Object.values(stored).map((n) => summary(n.service, { preview: n.text, updatedByName: n.updatedByName })) });
    }

    const service = decodeURIComponent(/^\/api\/v1\/services\/(.+)\/notes$/.exec(request.path)?.[1] ?? "");

    if (request.method === "PUT") {
      const text = (request.body as { text: string }).text;
      onWrite?.(service, text);

      return json(200, text === "" ? { service, text: "" } : notes(service, { text }));
    }

    return json(200, stored[service] ?? { service, text: "" });
  });
}

const ROUTE = "/lessons?tab=notes";

describe("the service notes tab", () => {
  it("is reached from the tab list", async () => {
    const user = userEvent.setup();
    const { handler } = server({ billing: notes("billing") });

    renderApp({ me: VIEWER, route: "/lessons?tab=memory", handler: (r) => (new URL(r.url).pathname === "/api/v1/memory/entries" ? json(200, { entries: [] }) : handler(r)) });

    await user.click(await screen.findByRole("tab", { name: "Service notes" }));

    expect(await screen.findByRole("region", { name: "Services with notes" })).toBeInTheDocument();
    expect(screen.getByRole("tabpanel")).toHaveAccessibleName("Service notes");
  });

  it("lists the services that have notes, with the start of the notes and who changed them", async () => {
    const { handler } = server({ billing: notes("billing", { text: "<b>Quiet</b> at night." }), "checkout-api": notes("checkout-api", { updatedByName: undefined }) });
    renderApp({ me: VIEWER, route: ROUTE, handler });

    const table = await screen.findByRole("region", { name: "Services with notes" });
    const rows = within(table).getAllByRole("row");

    expect(rows).toHaveLength(3);
    expect(within(rows[1] as HTMLElement).getByText("<b>Quiet</b> at night.")).toBeInTheDocument();
    expect(rows[1]?.querySelector("b")).toBeNull();
    expect(within(rows[1] as HTMLElement).getByText("by Ada Lovelace")).toBeInTheDocument();
    expect(within(rows[2] as HTMLElement).getByText("by an API token")).toBeInTheDocument();
  });

  it("says when no service has notes", async () => {
    const { handler } = server({});

    const { unmount } = renderApp({ me: ADMIN, route: ROUTE, handler });
    expect(await screen.findByRole("heading", { name: "No service has notes yet" })).toBeInTheDocument();
    expect(screen.getByText("Type a service name above to write the first notes.")).toBeInTheDocument();

    unmount();
    renderApp({ me: VIEWER, route: ROUTE, handler });
    expect(await screen.findByText("An admin can write notes about a service.")).toBeInTheDocument();
  });

  it("shows more services with the place the page before ended", async () => {
    const user = userEvent.setup();
    const { handler, seen } = recording((request) =>
      request.search.get("after") ? json(200, { services: [summary("orders-api")] }) : json(200, { services: [summary("billing")], nextAfter: "billing" }),
    );

    renderApp({ me: VIEWER, route: ROUTE, handler });

    await user.click(await screen.findByRole("button", { name: "Show more services" }));

    expect(await screen.findByRole("button", { name: "Open the notes about orders-api" })).toBeInTheDocument();
    expect(seen.filter((r) => r.path === LIST).at(-1)?.search.get("after")).toBe("billing");
  });

  it("opens the notes of a service from the list, with who changed them last and when", async () => {
    const user = userEvent.setup();
    const { handler, seen } = server({ billing: notes("billing", { text: "Billing is quiet at night." }) });

    renderApp({ me: ADMIN, route: ROUTE, handler });

    await user.click(await screen.findByRole("button", { name: "Open the notes about billing" }));

    const form = await screen.findByRole("form", { name: "Edit the notes about billing" });
    expect(within(form).getByRole("textbox", { name: "Notes" })).toHaveValue("Billing is quiet at night.");
    expect(screen.getByRole("heading", { name: "Notes about billing" })).toBeInTheDocument();
    expect(screen.getByText(/Last changed by Ada Lovelace on/)).toBeInTheDocument();
    expect(seen.some((r) => r.path === "/api/v1/services/billing/notes")).toBe(true);
  });

  it("opens the notes of a service that is typed, also one with no notes yet", async () => {
    const user = userEvent.setup();
    const { handler, seen } = server({});

    renderApp({ me: ADMIN, route: ROUTE, handler });

    await user.type(await screen.findByRole("combobox", { name: "Service" }), "team/checkout");
    await user.click(screen.getByRole("button", { name: "Open notes" }));

    expect(await screen.findByText("Nobody has written notes about this service yet.")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Notes" })).toHaveValue("");
    expect(seen.some((r) => r.path === "/api/v1/services/team%2Fcheckout/notes")).toBe(true);
  });

  it("opens on the service in the address", async () => {
    const { handler } = server({ billing: notes("billing") });
    renderApp({ me: ADMIN, route: `${ROUTE}&service=billing`, handler });

    expect(await screen.findByRole("textbox", { name: "Notes" })).toHaveValue("Notes about billing.");
    expect(screen.getByRole("combobox", { name: "Service" })).toHaveValue("billing");
  });

  it("saves the notes as plain text", async () => {
    const user = userEvent.setup();
    const writes: [string, string][] = [];
    const { handler, seen } = server({ billing: notes("billing") }, (service, text) => writes.push([service, text]));

    renderApp({ me: ADMIN, route: `${ROUTE}&service=billing`, handler });

    const text = await screen.findByRole("textbox", { name: "Notes" });
    expect(screen.getByRole("button", { name: "Save notes" })).toBeInTheDocument();

    await user.clear(text);
    await user.type(text, "  <b>Busy</b> now.  ");
    await user.click(screen.getByRole("button", { name: "Save notes" }));

    expect(await screen.findByText("Notes saved. The memory source shows them to the model.")).toBeInTheDocument();
    expect(writes).toEqual([["billing", "<b>Busy</b> now."]]);
    expect(seen.find((r) => r.method === "PUT")?.csrf).toBe(VIEWER.csrfToken);
  });

  it("deletes the notes only after a confirm step in the page, when the text is emptied", async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, "confirm");
    const writes: [string, string][] = [];
    const { handler } = server({ billing: notes("billing") }, (service, text) => writes.push([service, text]));

    renderApp({ me: ADMIN, route: `${ROUTE}&service=billing`, handler });

    await user.clear(await screen.findByRole("textbox", { name: "Notes" }));
    await user.click(screen.getByRole("button", { name: "Delete notes" }));

    const group = screen.getByRole("group", { name: "Delete these notes" });
    expect(within(group).getByText(/The notes about billing are removed/)).toBeInTheDocument();
    expect(writes).toEqual([]);

    await user.click(within(group).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("group", { name: "Delete these notes" })).not.toBeInTheDocument();
    expect(writes).toEqual([]);

    await user.click(screen.getByRole("button", { name: "Delete notes" }));
    await user.click(within(screen.getByRole("group", { name: "Delete these notes" })).getByRole("button", { name: "Delete notes" }));

    expect(await screen.findByText("Notes deleted.")).toBeInTheDocument();
    expect(writes).toEqual([["billing", ""]]);
    expect(confirm).not.toHaveBeenCalled();
  });

  it("shows why the notes were refused and keeps what was typed", async () => {
    const user = userEvent.setup();
    const { handler } = recording((request) => {
      if (request.method === "PUT") {
        return validationProblem([{ field: "text", message: "The notes are longer than 4000 characters" }]);
      }

      return request.path === LIST ? json(200, { services: [] }) : json(200, { service: "billing", text: "Old." });
    });

    renderApp({ me: ADMIN, route: `${ROUTE}&service=billing`, handler });

    const text = await screen.findByRole("textbox", { name: "Notes" });
    await user.type(text, " more");
    await user.click(screen.getByRole("button", { name: "Save notes" }));

    expect(await screen.findByText("The notes are longer than 4000 characters")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Notes" })).toHaveValue("Old. more");
  });

  it("gives a viewer and a responder the notes to read, without a form", async () => {
    for (const me of [VIEWER, RESPONDER]) {
      const { handler } = server({ billing: notes("billing", { text: "<i>Read</i> me." }) });
      const { unmount } = renderApp({ me, route: `${ROUTE}&service=billing`, handler });

      expect(await screen.findByText("<i>Read</i> me.", { selector: "p" })).toBeInTheDocument();
      expect(screen.getByText(/Changing notes needs the admin role/)).toBeInTheDocument();
      expect(screen.queryByRole("textbox", { name: "Notes" })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Save notes" })).not.toBeInTheDocument();

      unmount();
    }
  });

  it("has no accessibility violations", async () => {
    const { handler } = server({ billing: notes("billing"), orders: notes("orders") });
    const { container } = renderApp({ me: ADMIN, route: `${ROUTE}&service=billing`, handler });

    await screen.findByRole("form", { name: "Edit the notes about billing" });

    expect(await wcagViolations(container)).toEqual([]);
  });
});
