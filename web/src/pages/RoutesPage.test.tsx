import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { VIEWER, json, renderApp } from "../test/helpers";
import { ADMIN, RESPONDER, recording, route, rowAt, sampleRoutes } from "../test/rules";

describe("the routes page", () => {
  it("lists the routes in the order they are tried, with their layer and status", async () => {
    const { handler } = recording(() => json(200, { routes: sampleRoutes() }));
    renderApp({ me: ADMIN, route: "/routes", handler });

    const table = await screen.findByRole("region", { name: "Routes" });
    const rows = within(table).getAllByRole("row");

    expect(rows).toHaveLength(4);
    expect(within(rowAt(rows, 1)).getByRole("rowheader", { name: "payments" })).toBeInTheDocument();
    expect(within(rowAt(rows, 1)).getByText("1")).toBeInTheDocument();
    expect(within(rowAt(rows, 1)).getByText("Service payments-*")).toBeInTheDocument();
    expect(within(rowAt(rows, 2)).getByRole("rowheader", { name: "default" })).toBeInTheDocument();
    expect(within(rowAt(rows, 2)).getByText("Every event")).toBeInTheDocument();

    expect(within(rowAt(rows, 3)).getByText("Turned off")).toBeInTheDocument();
    expect(within(rowAt(rows, 3)).getByText("Not tried")).toBeInTheDocument();
    expect(within(rowAt(rows, 3)).getByText("Strategy generic-alert, mode read-only")).toBeInTheDocument();
    expect(within(rowAt(rows, 3)).getByText("Outputs slack-thread")).toBeInTheDocument();
  });

  it("gives the config file routes no controls and a runtime route a way to change and delete it", async () => {
    const { handler } = recording(() => json(200, { routes: sampleRoutes() }));
    renderApp({ me: ADMIN, route: "/routes", handler });

    const table = await screen.findByRole("region", { name: "Routes" });
    const rows = within(table).getAllByRole("row");

    expect(within(rowAt(rows, 1)).queryByRole("link")).not.toBeInTheDocument();
    expect(within(rowAt(rows, 1)).queryByRole("button")).not.toBeInTheDocument();
    expect(within(rowAt(rows, 3)).getByRole("link", { name: "Change route checkout" })).toHaveAttribute("href", "/routes/checkout");
    expect(within(rowAt(rows, 3)).getByRole("button", { name: "Delete route checkout" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Add a route/ })).toHaveAttribute("href", "/routes/new");
  });

  it("flags a runtime route that a file route shadows and one that fails the checks", async () => {
    const { handler } = recording(() =>
      json(200, {
        routes: [
          route({ name: "payments", layer: "file", readOnly: true, position: 1 }),
          route({ name: "payments", status: "shadowed", statusReason: "a rule in the config file has the same name", position: undefined }),
          route({ name: "broken", status: "invalid", statusReason: 'routes.broken.sources[0]: source "gone" is not defined', position: undefined }),
        ],
      }),
    );

    renderApp({ me: ADMIN, route: "/routes", handler });

    const table = await screen.findByRole("region", { name: "Routes" });
    const rows = within(table).getAllByRole("row");

    expect(within(rowAt(rows, 2)).getByText("Shadowed")).toBeInTheDocument();
    expect(within(rowAt(rows, 2)).queryByRole("link", { name: /Change/ })).not.toBeInTheDocument();
    expect(within(rowAt(rows, 2)).getByRole("button", { name: "Delete route payments" })).toBeInTheDocument();
    expect(within(rowAt(rows, 3)).getByText("Invalid")).toBeInTheDocument();
    expect(within(rowAt(rows, 3)).getByText(/source "gone" is not defined/)).toBeInTheDocument();
  });

  it("tells a responder that routes are for admins", async () => {
    const { handler } = recording(() => json(200, { routes: sampleRoutes() }));
    renderApp({ me: RESPONDER, route: "/routes", handler });

    await screen.findByRole("region", { name: "Routes" });

    expect(screen.getByText("Adding and changing routes needs the admin role.")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Add a route/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Delete/ })).not.toBeInTheDocument();
  });

  it("deletes a route after asking, with the CSRF token", async () => {
    const user = userEvent.setup();
    let routes = sampleRoutes();

    const { handler, seen } = recording((request) => {
      if (request.method === "DELETE") {
        routes = routes.filter((r) => r.name !== "checkout");

        return new Response(null, { status: 204 });
      }

      return json(200, { routes });
    });

    renderApp({ me: ADMIN, route: "/routes", handler });

    await user.click(await screen.findByRole("button", { name: "Delete route checkout" }));
    expect(screen.getByText("Delete checkout?")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Delete route checkout" }));

    expect(await screen.findByText("Route checkout deleted.")).toBeInTheDocument();

    const request = seen.find((r) => r.method === "DELETE");
    expect(request?.path).toBe("/api/v1/routes/checkout");
    expect(request?.csrf).toBe(VIEWER.csrfToken);
  });

  it("shows a viewer the list without controls", async () => {
    const { handler } = recording(() => json(200, { routes: sampleRoutes() }));
    renderApp({ me: VIEWER, route: "/routes", handler });

    await screen.findByRole("region", { name: "Routes" });

    expect(screen.queryByRole("button", { name: /Delete/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Change/ })).not.toBeInTheDocument();
  });
});
