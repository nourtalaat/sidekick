import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { SEARCH_MAX_LENGTH } from "../pages/investigations/filters";
import { summary } from "../test/fixtures";
import { json, renderApp } from "../test/helpers";
import { routes } from "../test/streams";

/** A fake server for the list that keeps the queries it got. */
function listServer() {
  const queries: URLSearchParams[] = [];

  const handler = routes({
    "GET /api/v1/investigations": (request) => {
      queries.push(new URL(request.url).searchParams);

      return json(200, { investigations: [summary({ id: "inv-1", title: "Checkout p99 latency above 2s" })] });
    },
  });

  return { handler, queries };
}

const searchBox = () => screen.findByRole("searchbox", { name: "Search investigations" });

describe("the search box in the header", () => {
  it("is a working search form, with nothing that says it is off", async () => {
    renderApp({ route: "/investigations" });

    const box = await searchBox();

    expect(box).toBeEnabled();
    expect(box).not.toHaveAttribute("title");
    expect(box).toHaveAttribute("maxlength", String(SEARCH_MAX_LENGTH));
    expect(screen.getByRole("search", { name: "Search investigations" })).toContainElement(box);
  });

  it("opens the list with the text when Enter is pressed", async () => {
    const user = userEvent.setup();
    const { handler, queries } = listServer();
    renderApp({ route: "/dashboard", handler });

    await user.type(await searchBox(), "checkout{Enter}");

    expect(await screen.findByRole("heading", { name: "Investigations", level: 1 })).toBeInTheDocument();
    await screen.findByRole("table");
    expect(queries.at(-1)!.get("q")).toBe("checkout");
    expect(screen.getByLabelText("Alert title")).toHaveValue("checkout");
  });

  it("replaces the other filters of the list, and the compact rows", async () => {
    const user = userEvent.setup();
    const { handler, queries } = listServer();
    renderApp({ route: "/investigations?verdict=true_positive&status=final&service=db&compact=1", handler });
    await screen.findByRole("table");

    await user.type(await searchBox(), "disk{Enter}");

    await expect.poll(() => queries.at(-1)!.get("q")).toBe("disk");
    for (const name of ["verdict", "status", "service", "route", "model", "from"]) {
      expect(queries.at(-1)!.has(name)).toBe(false);
    }
    expect(screen.getByLabelText("Verdict")).toHaveValue("");
    expect(screen.getByLabelText("Service")).toHaveValue("");
    expect(screen.getByRole("checkbox", { name: "Compact rows" })).not.toBeChecked();
  });

  it("trims the text", async () => {
    const user = userEvent.setup();
    const { handler, queries } = listServer();
    renderApp({ route: "/investigations", handler });
    await screen.findByRole("table");

    await user.type(await searchBox(), "   disk full   {Enter}");

    await expect.poll(() => queries.at(-1)!.get("q")).toBe("disk full");
  });

  it("cuts a text that is longer than the list field takes", async () => {
    const user = userEvent.setup();
    const { handler, queries } = listServer();
    renderApp({ route: "/investigations", handler });
    await screen.findByRole("table");

    await user.click(await searchBox());
    await user.paste("a".repeat(SEARCH_MAX_LENGTH + 50));
    await user.keyboard("{Enter}");

    await expect.poll(() => queries.at(-1)!.get("q")).toBe("a".repeat(SEARCH_MAX_LENGTH));
    expect(screen.getByLabelText("Alert title")).toHaveValue("a".repeat(SEARCH_MAX_LENGTH));
  });

  it.each([
    { name: "an empty text", text: "" },
    { name: "only spaces", text: "   " },
  ])("clears the filters of the list on $name", async ({ text }) => {
    const user = userEvent.setup();
    const { handler, queries } = listServer();
    renderApp({ route: "/investigations?q=old&verdict=true_positive", handler });
    await screen.findByRole("table");

    const box = await searchBox();
    await user.clear(box);
    if (text) {
      await user.type(box, text);
    }
    await user.keyboard("{Enter}");

    await expect.poll(() => queries.at(-1)!.has("q")).toBe(false);
    expect(queries.at(-1)!.has("verdict")).toBe(false);
    expect(screen.getByLabelText("Alert title")).toHaveValue("");
    expect(box).toHaveValue("");
  });

  it("opens the list from another page on an empty submit", async () => {
    const user = userEvent.setup();
    const { handler } = listServer();
    renderApp({ route: "/dashboard", handler });

    await user.type(await searchBox(), "{Enter}");

    expect(await screen.findByRole("heading", { name: "Investigations", level: 1 })).toBeInTheDocument();
  });

  it("changes nothing while it is typed in", async () => {
    const user = userEvent.setup();
    const { handler, queries } = listServer();
    renderApp({ route: "/investigations?q=checkout", handler });
    await screen.findByRole("table");
    const before = queries.length;

    await user.type(await searchBox(), " disk");

    expect(screen.getByLabelText("Alert title")).toHaveValue("checkout");
    expect(queries).toHaveLength(before);
  });

  it("shows the text of the address, and so does the alert title field of the list", async () => {
    const { handler } = listServer();
    renderApp({ route: "/investigations?q=checkout", handler });

    expect(await searchBox()).toHaveValue("checkout");
    expect(screen.getByLabelText("Alert title")).toHaveValue("checkout");
  });

  it("follows the list when its own field is applied and cleared", async () => {
    const user = userEvent.setup();
    const { handler } = listServer();
    renderApp({ route: "/investigations", handler });
    await screen.findByRole("table");

    await user.type(screen.getByLabelText("Alert title"), "disk");
    await user.click(screen.getByRole("button", { name: "Apply" }));
    await expect.poll(async () => ((await searchBox()) as HTMLInputElement).value).toBe("disk");

    await user.click(screen.getByRole("button", { name: "Clear" }));
    await expect.poll(async () => ((await searchBox()) as HTMLInputElement).value).toBe("");
  });

  it("is empty on a page that is not the list, even when its address has q", async () => {
    renderApp({ route: "/lessons?q=retry" });

    expect(await searchBox()).toHaveValue("");
  });

  it("empties when the user goes to another page, and shows the text again on the way back", async () => {
    const user = userEvent.setup();
    const { handler } = listServer();
    renderApp({ route: "/investigations?q=checkout", handler });
    await screen.findByRole("table");

    await user.click(screen.getByRole("link", { name: "Dashboard" }));
    await screen.findByRole("heading", { name: "Dashboard", level: 1 });
    expect(await searchBox()).toHaveValue("");

    await user.type(await searchBox(), "disk{Enter}");
    expect(await searchBox()).toHaveValue("disk");
  });
});
