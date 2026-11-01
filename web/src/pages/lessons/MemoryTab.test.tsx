import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { MemoryEntry } from "../../api/memory";
import { VIEWER, json, renderApp, wcagViolations } from "../../test/helpers";
import { ADMIN, RESPONDER, codedProblem, recording, validationProblem } from "../../test/rules";

const ID = "0192d3a4-0000-7000-8000-0000000000e1";

function entry(over: Partial<MemoryEntry> = {}): MemoryEntry {
  return {
    investigationId: ID,
    service: "checkout-api",
    alertName: "CheckoutLatencyP99",
    verdict: "true_positive",
    summary: "p99 latency rose after a deploy.",
    cause: "Deploy 4f2c1a added a slow query.",
    confirmedAt: "2026-10-22T10:00:00Z",
    disabled: false,
    ...over,
  };
}

function list(entries: MemoryEntry[], extra: Record<string, unknown> = {}) {
  return json(200, { entries, ...extra });
}

const ROUTE = "/lessons?tab=memory";

describe("the memory tab", () => {
  it("is reached from the tab list, and keeps the tab in the address", async () => {
    const user = userEvent.setup();
    const { handler, seen } = recording((request) => (request.path === "/api/v1/memory/entries" ? list([entry()]) : json(200, { lessons: [], counts: { proposed: 0, active: 0, disabled: 0, rejected: 0 } })));

    renderApp({ me: VIEWER, route: "/lessons", handler });

    await user.click(await screen.findByRole("tab", { name: "Memory" }));

    expect(await screen.findByRole("article")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Memory" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tabpanel")).toHaveAccessibleName("Memory");
    expect(seen.some((r) => r.path === "/api/v1/memory/entries")).toBe(true);
  });

  it("lists entries with the confirmed verdict, summary, cause, alert and a link to the investigation, as plain text", async () => {
    const { handler } = recording(() => list([entry({ summary: "<b>Slow</b> & loud." }), entry({ investigationId: "x2", verdict: "flapping", cause: undefined, service: undefined, alertName: undefined })]));
    renderApp({ me: VIEWER, route: ROUTE, handler });

    const [first, second] = await screen.findAllByRole("article");
    if (!first || !second) {
      throw new Error("want two entries");
    }

    expect(within(first).getByText("Real issue")).toBeInTheDocument();
    expect(within(first).getByText("<b>Slow</b> & loud.")).toBeInTheDocument();
    expect(first.querySelector("b")).toBeNull();
    expect(within(first).getByText("Deploy 4f2c1a added a slow query.")).toBeInTheDocument();
    expect(within(first).getByText("checkout-api")).toBeInTheDocument();
    expect(within(first).getByText("CheckoutLatencyP99")).toBeInTheDocument();
    expect(within(first).getByRole("link", { name: "Open investigation 000000e1" })).toHaveAttribute("href", `/investigations/${ID}`);

    expect(within(second).getByText("Flapping")).toBeInTheDocument();
    expect(within(second).queryByText("Cause:")).not.toBeInTheDocument();
    expect(within(second).queryByText("Service")).not.toBeInTheDocument();
  });

  it("marks a disabled entry", async () => {
    const { handler } = recording(() => list([entry({ disabled: true })]));
    renderApp({ me: VIEWER, route: ROUTE, handler });

    expect(within(await screen.findByRole("article")).getByText("Disabled")).toBeInTheDocument();
  });

  it("sends the filters and keeps them in the address", async () => {
    const user = userEvent.setup();
    const { handler, seen } = recording(() => list([entry()]));

    renderApp({ me: VIEWER, route: ROUTE, handler });
    await screen.findByRole("article");

    const form = screen.getByRole("form", { name: "Filter the memory entries" });
    await user.type(within(form).getByRole("textbox", { name: "Service" }), "checkout-api");
    await user.selectOptions(within(form).getByRole("combobox", { name: "Verdict" }), "false_positive");
    await user.type(within(form).getByRole("textbox", { name: "Words" }), "pool");
    await user.selectOptions(within(form).getByRole("combobox", { name: "State" }), "disabled");
    await user.click(within(form).getByRole("button", { name: "Filter" }));

    await vi.waitFor(() => {
      const last = seen.filter((r) => r.path === "/api/v1/memory/entries").at(-1);
      expect(last?.search.get("service")).toBe("checkout-api");
      expect(last?.search.get("verdict")).toBe("false_positive");
      expect(last?.search.get("q")).toBe("pool");
      expect(last?.search.get("disabled")).toBe("true");
    });

    await user.selectOptions(screen.getByRole("combobox", { name: "State" }), "enabled");
    await user.click(screen.getByRole("button", { name: "Filter" }));

    await vi.waitFor(() => {
      expect(seen.filter((r) => r.path === "/api/v1/memory/entries").at(-1)?.search.get("disabled")).toBe("false");
    });

    await user.click(screen.getByRole("button", { name: "Clear filters" }));

    await vi.waitFor(() => {
      const last = seen.filter((r) => r.path === "/api/v1/memory/entries").at(-1);
      expect(last?.search.get("service")).toBeNull();
      expect(last?.search.get("disabled")).toBeNull();
    });
  });

  it("starts from the filters in the address", async () => {
    const { handler, seen } = recording(() => list([entry()]));
    renderApp({ me: VIEWER, route: "/lessons?tab=memory&service=db&verdict=flapping&q=disk&disabled=enabled", handler });

    await screen.findByRole("article");

    const request = seen.find((r) => r.path === "/api/v1/memory/entries");
    expect(request?.search.get("service")).toBe("db");
    expect(request?.search.get("verdict")).toBe("flapping");
    expect(request?.search.get("q")).toBe("disk");
    expect(request?.search.get("disabled")).toBe("false");
    expect(screen.getByRole("combobox", { name: "Verdict" })).toHaveValue("flapping");
  });

  it("says when memory is empty or nothing matches", async () => {
    const { handler } = recording(() => list([]));
    const { unmount } = renderApp({ me: VIEWER, route: ROUTE, handler });

    expect(await screen.findByRole("heading", { name: "Memory is empty" })).toBeInTheDocument();

    unmount();
    renderApp({ me: VIEWER, route: `${ROUTE}&q=zzz`, handler });

    expect(await screen.findByRole("heading", { name: "No entries match these filters" })).toBeInTheDocument();
  });

  it("shows older entries with the cursor of the page before", async () => {
    const user = userEvent.setup();
    const { handler, seen } = recording((request) =>
      request.search.get("cursor") ? list([entry({ investigationId: "x3", summary: "An older entry." })]) : list([entry()], { nextCursor: "next-page" }),
    );

    renderApp({ me: VIEWER, route: ROUTE, handler });

    await user.click(await screen.findByRole("button", { name: "Show older entries" }));

    expect(await screen.findByText("An older entry.")).toBeInTheDocument();
    expect(seen.filter((r) => r.path === "/api/v1/memory/entries").at(-1)?.search.get("cursor")).toBe("next-page");
  });

  it("gives a viewer and a responder the list without the changes", async () => {
    for (const me of [VIEWER, RESPONDER]) {
      const { handler } = recording(() => list([entry()]));
      const { unmount } = renderApp({ me, route: ROUTE, handler });

      await screen.findByRole("article");

      expect(screen.getByText(/Changing entries needs the admin role/)).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: /^Edit memory entry/ })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: /^Disable memory entry/ })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: /^Delete memory entry/ })).not.toBeInTheDocument();

      unmount();
    }
  });

  it("edits the summary and cause, and says the edit lasts until the confirmation changes", async () => {
    const user = userEvent.setup();
    const { handler, seen } = recording((request) => (request.method === "PUT" ? json(200, entry({ summary: "New summary." })) : list([entry()])));

    renderApp({ me: ADMIN, route: ROUTE, handler });

    await user.click(await screen.findByRole("button", { name: /^Edit memory entry/ }));

    const form = screen.getByRole("form", { name: "Edit memory entry" });
    expect(within(form).getByText(/Your edit lasts until the confirmation of this investigation changes/)).toBeInTheDocument();
    expect(within(form).getByText(/the text you wrote is replaced/)).toBeInTheDocument();
    expect(within(form).getByText(/To keep an entry out of prompts for good, disable it/)).toBeInTheDocument();

    const summary = within(form).getByRole("textbox", { name: "Summary" });
    expect(summary).toHaveValue("p99 latency rose after a deploy.");

    await user.clear(summary);
    await user.type(summary, "  New summary.  ");
    await user.clear(within(form).getByRole("textbox", { name: "Cause" }));
    await user.click(within(form).getByRole("button", { name: "Save entry" }));

    expect(await screen.findByText(/Entry saved\. It stays as you wrote it until the confirmation of its investigation changes\./)).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Edit memory entry" })).not.toBeInTheDocument();

    const put = seen.find((r) => r.method === "PUT");
    expect(put?.path).toBe(`/api/v1/memory/entries/${ID}`);
    expect(put?.body).toEqual({ summary: "New summary.", disabled: false });
    expect(put?.csrf).toBe(VIEWER.csrfToken);
  });

  it("shows the problem the server found under its field and keeps what was typed", async () => {
    const user = userEvent.setup();
    const { handler } = recording((request) =>
      request.method === "PUT" ? validationProblem([{ field: "summary", message: "The summary is required" }]) : list([entry()]),
    );

    renderApp({ me: ADMIN, route: ROUTE, handler });

    await user.click(await screen.findByRole("button", { name: /^Edit memory entry/ }));
    await user.clear(screen.getByRole("textbox", { name: "Summary" }));
    await user.type(screen.getByRole("textbox", { name: "Cause" }), " more");
    await user.click(screen.getByRole("button", { name: "Save entry" }));

    expect(await screen.findByText("The summary is required")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Cause" })).toHaveValue("Deploy 4f2c1a added a slow query. more");
  });

  it("disables an entry and enables it again, keeping its text", async () => {
    const user = userEvent.setup();
    let current = entry();

    const { handler, seen } = recording((request) => {
      if (request.method === "PUT") {
        current = { ...current, disabled: (request.body as { disabled: boolean }).disabled };

        return json(200, current);
      }

      return list([current]);
    });

    renderApp({ me: ADMIN, route: ROUTE, handler });

    await user.click(await screen.findByRole("button", { name: /^Disable memory entry/ }));
    expect(await screen.findByText("Entry disabled. Prompts never show it and searches never find it.")).toBeInTheDocument();

    await user.click(await screen.findByRole("button", { name: /^Enable memory entry/ }));
    expect(await screen.findByText("Entry enabled. Prompts can show it again.")).toBeInTheDocument();

    const puts = seen.filter((r) => r.method === "PUT");
    expect(puts[0]?.body).toEqual({ summary: "p99 latency rose after a deploy.", cause: "Deploy 4f2c1a added a slow query.", disabled: true });
    expect(puts[1]?.body).toEqual({ summary: "p99 latency rose after a deploy.", cause: "Deploy 4f2c1a added a slow query.", disabled: false });
  });

  it("shows a refused change", async () => {
    const user = userEvent.setup();
    const { handler } = recording((request) => (request.method === "PUT" ? codedProblem(404, "not_found", "No memory entry has this ID.") : list([entry()])));

    renderApp({ me: ADMIN, route: ROUTE, handler });

    await user.click(await screen.findByRole("button", { name: /^Disable memory entry/ }));

    expect(await screen.findByText("No memory entry has this ID.")).toBeInTheDocument();
  });

  it("asks in the page before it deletes, says what a delete does, and deletes only when confirmed", async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, "confirm");
    let deleted = false;

    const { handler, seen } = recording((request) => {
      if (request.method === "DELETE") {
        deleted = true;

        return new Response(null, { status: 204 });
      }

      return list(deleted ? [] : [entry()]);
    });

    renderApp({ me: ADMIN, route: ROUTE, handler });

    await user.click(await screen.findByRole("button", { name: /^Delete memory entry/ }));

    const group = screen.getByRole("group", { name: "Delete this memory entry" });
    expect(within(group).getByText(/If the confirmation of its investigation changes later, Sidekick writes a new entry/)).toBeInTheDocument();
    expect(within(group).getByText(/disable it instead/)).toBeInTheDocument();

    await user.click(within(group).getByRole("button", { name: "Cancel" }));
    expect(seen.some((r) => r.method === "DELETE")).toBe(false);

    await user.click(screen.getByRole("button", { name: /^Delete memory entry/ }));
    await user.click(within(screen.getByRole("group", { name: "Delete this memory entry" })).getByRole("button", { name: "Delete memory entry" }));

    expect(await screen.findByText("Entry deleted.")).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "Memory is empty" })).toBeInTheDocument();
    expect(seen.find((r) => r.method === "DELETE")?.path).toBe(`/api/v1/memory/entries/${ID}`);
    expect(confirm).not.toHaveBeenCalled();
  });

  it("has no accessibility violations", async () => {
    const { handler } = recording(() => list([entry(), entry({ investigationId: "x2", disabled: true, cause: undefined })]));
    const { container } = renderApp({ me: ADMIN, route: ROUTE, handler });

    await screen.findAllByRole("article");

    expect(await wcagViolations(container)).toEqual([]);
  });
});
