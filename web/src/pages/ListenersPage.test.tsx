import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { VIEWER, json, renderApp } from "../test/helpers";
import { ADMIN, RESPONDER, listener, recording, rowAt, sampleListeners } from "../test/rules";

describe("the listeners page", () => {
  it("lists the listeners with their layer and status, and leaves the file ones without controls", async () => {
    const { handler } = recording(() => json(200, { listeners: sampleListeners() }));
    renderApp({ me: RESPONDER, route: "/listeners", handler });

    const table = await screen.findByRole("region", { name: "Listeners" });
    const rows = within(table).getAllByRole("row");

    expect(rows).toHaveLength(4);
    expect(within(rowAt(rows, 1)).getByText("Config file", { selector: ".sk-badge" })).toBeInTheDocument();
    expect(within(rowAt(rows, 1)).getByText("Active")).toBeInTheDocument();
    expect(within(rowAt(rows, 2)).getByText("Added at runtime")).toBeInTheDocument();
    expect(within(rowAt(rows, 2)).getByText(/messages from Grafana/)).toBeInTheDocument();
    expect(within(rowAt(rows, 2)).getByText("(?i)firing")).toBeInTheDocument();
    expect(within(rowAt(rows, 3)).getByText("Shadowed")).toBeInTheDocument();
    expect(within(rowAt(rows, 3)).getByText("a rule in the config file has the same name")).toBeInTheDocument();

    expect(within(rowAt(rows, 1)).queryByRole("link")).not.toBeInTheDocument();
    expect(within(rowAt(rows, 1)).queryByRole("button")).not.toBeInTheDocument();
    expect(within(rowAt(rows, 2)).getByRole("link", { name: "Change listener ops" })).toHaveAttribute("href", "/listeners/ops");
    expect(within(rowAt(rows, 2)).getByRole("button", { name: "Delete listener ops" })).toBeInTheDocument();
    expect(within(rowAt(rows, 3)).queryByRole("link", { name: /Change/ })).not.toBeInTheDocument();
    expect(within(rowAt(rows, 3)).getByRole("button", { name: "Delete listener alerts" }), "a shadowed copy can still be deleted").toBeInTheDocument();
  });

  it("shows a viewer the list without the buttons", async () => {
    const { handler } = recording(() => json(200, { listeners: sampleListeners() }));
    renderApp({ me: VIEWER, route: "/listeners", handler });

    await screen.findByRole("region", { name: "Listeners" });

    expect(screen.getByText("Adding and changing listeners needs the responder role.")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Add a listener/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Delete/ })).not.toBeInTheDocument();
  });

  it("offers a responder the button to add a listener", async () => {
    const { handler } = recording(() => json(200, { listeners: [] }));
    renderApp({ me: RESPONDER, route: "/listeners", handler });

    expect(await screen.findByRole("link", { name: /Add a listener/ })).toHaveAttribute("href", "/listeners/new");
    expect(await screen.findByRole("heading", { name: "No listeners yet" })).toBeInTheDocument();
  });

  it("asks before it deletes, and deletes with the CSRF token", async () => {
    const user = userEvent.setup();
    let listeners = sampleListeners();

    const { handler, seen } = recording((request) => {
      if (request.method === "DELETE") {
        listeners = listeners.filter((l) => l.name !== "ops");

        return new Response(null, { status: 204 });
      }

      return json(200, { listeners });
    });

    renderApp({ me: RESPONDER, route: "/listeners", handler });

    await user.click(await screen.findByRole("button", { name: "Delete listener ops" }));
    expect(screen.getByText("Delete ops?")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.getByRole("button", { name: "Delete listener ops" })).toBeInTheDocument();
    expect(seen.some((r) => r.method === "DELETE")).toBe(false);

    await user.click(screen.getByRole("button", { name: "Delete listener ops" }));
    await user.click(screen.getByRole("button", { name: "Delete listener ops" }));

    expect(await screen.findByText("Listener ops deleted.")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Change listener ops" })).not.toBeInTheDocument();

    const request = seen.find((r) => r.method === "DELETE");
    expect(request?.path).toBe("/api/v1/listeners/ops");
    expect(request?.csrf).toBe(VIEWER.csrfToken);
  });

  it("says why a delete was refused", async () => {
    const user = userEvent.setup();

    const { handler } = recording((request) =>
      request.method === "DELETE"
        ? json(403, { type: "about:blank", title: "Forbidden", status: 403, detail: "This needs the responder role." })
        : json(200, { listeners: [listener()] }),
    );

    renderApp({ me: RESPONDER, route: "/listeners", handler });

    await user.click(await screen.findByRole("button", { name: "Delete listener ops" }));
    await user.click(screen.getByRole("button", { name: "Delete listener ops" }));

    expect(await screen.findByRole("heading", { name: "Could not delete the listener" })).toBeInTheDocument();
    expect(screen.getByText("This needs the responder role.")).toBeInTheDocument();
  });

  it("is also open to an admin", async () => {
    const { handler } = recording(() => json(200, { listeners: [listener()] }));
    renderApp({ me: ADMIN, route: "/listeners", handler });

    expect(await screen.findByRole("link", { name: "Change listener ops" })).toBeInTheDocument();
  });
});
