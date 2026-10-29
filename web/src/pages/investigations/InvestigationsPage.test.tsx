import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { summary } from "../../test/fixtures";
import { json, renderApp } from "../../test/helpers";
import { routes } from "../../test/streams";

const FIRST_PAGE = [
  summary({ id: "inv-1", title: "Checkout p99 latency above 2s" }),
  summary({
    id: "inv-2",
    title: "Database disk almost full",
    service: "db",
    verdict: "false_positive",
    feedback: { correct: 0, wrong: 1, summary: "wrong" },
    watcher: { name: "health", status: "running" },
  }),
  summary({ id: "inv-3", title: undefined, verdict: undefined, status: "running", feedback: { correct: 0, wrong: 0, summary: "none" }, watcher: undefined }),
];

/** A fake server for the list that answers a page of two cursors, and keeps the queries it got. */
function listServer() {
  const queries: URLSearchParams[] = [];

  const handler = routes({
    "GET /api/v1/investigations": (request) => {
      const query = new URL(request.url).searchParams;
      queries.push(query);

      return query.get("cursor") === "next"
        ? json(200, { investigations: [summary({ id: "inv-4", title: "Second page row" })] })
        : json(200, { investigations: FIRST_PAGE, nextCursor: "next" });
    },
  });

  return { handler, queries };
}

describe("the investigation list", () => {
  it("shows a row for each investigation, with the verdict first", async () => {
    const { handler } = listServer();
    renderApp({ route: "/investigations", handler });

    const table = await screen.findByRole("table", { name: /Investigations/ });
    const rows = within(table).getAllByRole("row");

    expect(rows).toHaveLength(4);
    expect(within(table).getAllByRole("columnheader")[0]).toHaveTextContent("Verdict");
    expect(within(rows[1]!).getByText("Real issue")).toBeInTheDocument();
    expect(within(rows[1]!).getByRole("link", { name: "Checkout p99 latency above 2s" })).toHaveAttribute("href", "/investigations/inv-1");
    expect(within(rows[1]!).getByText("Correct")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("Back up")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("False alarm")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("Wrong")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("Checking")).toBeInTheDocument();
    expect(within(rows[3]!).getByText("Investigating")).toBeInTheDocument();
    expect(within(rows[3]!).getByRole("link", { name: "(no title)" })).toBeInTheDocument();
  });

  it("loads the next page with the cursor when Load more is pressed", async () => {
    const user = userEvent.setup();
    const { handler, queries } = listServer();
    renderApp({ route: "/investigations", handler });

    await user.click(await screen.findByRole("button", { name: "Load more" }));

    expect(await screen.findByRole("link", { name: "Second page row" })).toBeInTheDocument();
    expect(queries.map((q) => q.get("cursor"))).toEqual([null, "next"]);
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("reads the filters from the URL and sends them", async () => {
    const { handler, queries } = listServer();
    renderApp({ route: "/investigations?q=checkout&verdict=true_positive&status=final&route=payments&service=checkout-api&model=big", handler });

    await screen.findByRole("table");

    const query = queries[0]!;
    expect(query.get("q")).toBe("checkout");
    expect(query.get("verdict")).toBe("true_positive");
    expect(query.get("status")).toBe("final");
    expect(query.get("route")).toBe("payments");
    expect(query.get("service")).toBe("checkout-api");
    expect(query.get("model")).toBe("big");
    expect(screen.getByLabelText("Alert title")).toHaveValue("checkout");
    expect(screen.getByLabelText("Verdict")).toHaveValue("true_positive");
  });

  it("ignores a filter value the API does not take", async () => {
    const { handler, queries } = listServer();
    renderApp({ route: "/investigations?verdict=bogus&status=odd&range=forever", handler });

    await screen.findByRole("table");

    expect(queries[0]!.get("verdict")).toBeNull();
    expect(queries[0]!.get("status")).toBeNull();
    expect(queries[0]!.get("from")).toBeNull();
  });

  it("applies a select at once and the text fields when the form is sent, and Clear removes them", async () => {
    const user = userEvent.setup();
    const { handler, queries } = listServer();
    renderApp({ route: "/investigations", handler });
    await screen.findByRole("table");

    await user.type(screen.getByLabelText("Alert title"), "disk");
    await user.selectOptions(screen.getByLabelText("Verdict"), "false_positive");

    await screen.findByRole("table");
    const last = () => queries.at(-1)!;
    await expect.poll(() => last().get("verdict")).toBe("false_positive");
    expect(last().get("q")).toBe("disk");

    await user.selectOptions(screen.getByLabelText("Started"), "24h");
    await expect.poll(() => last().get("from")).not.toBeNull();

    await user.click(screen.getByRole("button", { name: "Clear" }));
    await expect.poll(() => last().get("verdict")).toBeNull();
    expect(last().get("q")).toBeNull();
    expect(screen.getByLabelText("Alert title")).toHaveValue("");
  });

  it("keeps compact rows in the URL", async () => {
    const user = userEvent.setup();
    const { handler } = listServer();
    renderApp({ route: "/investigations", handler });

    const table = await screen.findByRole("table");
    expect(table).not.toHaveClass("sk-table--compact");

    await user.click(screen.getByRole("checkbox", { name: "Compact rows" }));

    expect(screen.getByRole("table")).toHaveClass("sk-table--compact");
  });

  it("starts compact when the URL says so", async () => {
    const { handler } = listServer();
    renderApp({ route: "/investigations?compact=1", handler });

    expect(await screen.findByRole("table")).toHaveClass("sk-table--compact");
    expect(screen.getByRole("checkbox", { name: "Compact rows" })).toBeChecked();
  });

  it("says so when nothing matches", async () => {
    renderApp({ route: "/investigations", handler: routes({ "GET /api/v1/investigations": () => json(200, { investigations: [] }) }) });

    expect(await screen.findByRole("heading", { name: "No investigations" })).toBeInTheDocument();
  });

  it("shows a failure with a retry", async () => {
    renderApp({
      route: "/investigations",
      handler: routes({
        "GET /api/v1/investigations": () =>
          new Response(JSON.stringify({ title: "Forbidden", status: 403, detail: "This needs the viewer role." }), {
            status: 403,
            headers: { "Content-Type": "application/problem+json" },
          }),
      }),
    });

    expect(await screen.findByRole("alert")).toHaveTextContent("Needs the viewer role");
  });
});
