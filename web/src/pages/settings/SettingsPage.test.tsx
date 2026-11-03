import { QueryClient } from "@tanstack/react-query";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { LONG_NOTE, sampleConfig } from "../../test/config";
import { VIEWER, json, problem, renderApp } from "../../test/helpers";
import { ADMIN, recording } from "../../test/rules";

const FILE_KEYS = ["acme", "budgets", "database", "model", "sources"];

function openSettings(route = "/settings") {
  const { handler, seen } = recording(() => json(200, sampleConfig()));

  return { ...renderApp({ me: ADMIN, route, handler }), requests: seen };
}

async function cardTitles(): Promise<string[]> {
  await screen.findByRole("heading", { name: "File settings" });

  return Array.from(document.querySelectorAll(".sk-config__card-title")).map((el) => el.textContent ?? "");
}

async function openEveryCard() {
  await screen.findByRole("heading", { name: "File settings" });
  for (const card of Array.from(document.querySelectorAll("details"))) {
    card.setAttribute("open", "");
  }
}

describe("the Settings page", () => {
  it("opens on the Config tab and says that the config is read-only", async () => {
    openSettings();

    expect(await screen.findByRole("heading", { level: 1, name: "Settings" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Config" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText(/Change it in the config file or the Helm values; it cannot be edited here\./)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Read about the config file" })).toHaveAttribute("href", expect.stringMatching(/^https:\/\/github\.com\/.*\/docs\/configuration\.md$/));
  });

  it("shows one closed card for each top-level key of the file, in the order the API gives them", async () => {
    openSettings();

    expect(await cardTitles()).toEqual(FILE_KEYS);
    expect(document.querySelectorAll("details[open]")).toHaveLength(0);
  });

  it("says in one line what is inside a closed card", async () => {
    openSettings();
    await cardTitles();

    expect(screen.getByText("2 settings: maxCostUSD, note")).toBeInTheDocument();
    expect(screen.getByText("1 setting: prod")).toBeInTheDocument();
  });

  it("shows nested maps, lists, a duration text and a long value, and copes with a key it has never seen", async () => {
    openSettings();
    await openEveryCard();

    expect(screen.getByText("idleTimeout")).toBeInTheDocument();
    expect(screen.getByText("10m0s")).toBeInTheDocument();
    expect(screen.getByText("maxConns")).toBeInTheDocument();
    expect(screen.getByText("10")).toBeInTheDocument();
    expect(screen.getByText("kubernetes")).toBeInTheDocument();
    expect(screen.getAllByRole("listitem").map((li) => li.textContent)).toEqual(expect.arrayContaining(["payments", "checkout"]));
    expect(screen.getByText("deep")).toBeInTheDocument();
    expect(screen.getByText("true")).toBeInTheDocument();
    expect(screen.getByText("Not set")).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Value of note" })).toHaveTextContent(LONG_NOTE);
  });

  it("shows the listeners and routes added at runtime, with links to change them", async () => {
    openSettings();

    const listeners = await screen.findByRole("region", { name: "Listeners" });
    expect(within(listeners).getByText("ops")).toBeInTheDocument();
    expect(within(listeners).getByText("On")).toBeInTheDocument();
    expect(within(listeners).getByText("U01SEED")).toBeInTheDocument();
    expect(within(listeners).getByText("#ops")).toBeInTheDocument();
    expect(within(listeners).getByRole("link", { name: "Change listeners" })).toHaveAttribute("href", "/listeners");

    const routes = screen.getByRole("region", { name: "Routes" });
    expect(within(routes).getByText("checkout")).toBeInTheDocument();
    expect(within(routes).getByText("Off")).toBeInTheDocument();
    expect(within(routes).getByText("3")).toBeInTheDocument();
    expect(within(routes).getByText("latency")).toBeInTheDocument();
    expect(within(routes).getByRole("link", { name: "Change routes" })).toHaveAttribute("href", "/routes");
  });

  it("shows a reference to a Secret as its name and key, and no other text of a secret field", async () => {
    openSettings();
    await openEveryCard();

    const references = Array.from(document.querySelectorAll("dd")).filter((dd) => dd.textContent?.startsWith("Secret"));
    expect(references.map((dd) => dd.textContent)).toEqual(["Secret sidekick-db, key url", "Secret sidekick-anthropic, key apiKey"]);

    expect(screen.queryByRole("button", { name: /copy/i })).not.toBeInTheDocument();
    expect(screen.getAllByRole("textbox")).toHaveLength(1);
    expect(screen.getByRole("textbox")).toHaveAccessibleName("Filter the config");
  });

  it("shows text from the server as text, never as HTML", async () => {
    const { handler } = recording(() => json(200, { file: { acme: { note: "<b>bold</b>" } }, runtime: { listeners: [], routes: [] } }));
    const { container } = renderApp({ me: ADMIN, route: "/settings", handler });
    await openEveryCard();

    expect(screen.getByText("<b>bold</b>")).toBeInTheDocument();
    expect(container.querySelector("b")).toBeNull();
  });

  it("says when nothing was added at runtime", async () => {
    const { handler } = recording(() => json(200, { file: { acme: {} }, runtime: { listeners: [], routes: [] } }));
    renderApp({ me: ADMIN, route: "/settings", handler });

    expect(await screen.findByText("No listeners were added at runtime.")).toBeInTheDocument();
    expect(screen.getByText("No routes were added at runtime.")).toBeInTheDocument();
  });

  describe("the filter", () => {
    it("narrows the cards to the keys and values that match, ignoring case, and opens them", async () => {
      const user = userEvent.setup();
      openSettings();
      await cardTitles();

      await user.type(screen.getByRole("textbox", { name: "Filter the config" }), "KUBERNETES");

      expect(await cardTitles()).toEqual(["sources"]);
      expect(document.querySelectorAll("details[open]")).toHaveLength(1);
      expect(screen.getByText("kubernetes")).toBeInTheDocument();
      expect(screen.queryByText("namespaces")).not.toBeInTheDocument();
      expect(screen.getByRole("status")).toHaveTextContent("1 of 7 sections match.");
      expect(screen.queryByRole("region", { name: "Listeners" })).not.toBeInTheDocument();
    });

    it("keeps a whole card when its key matches", async () => {
      const user = userEvent.setup();
      openSettings();
      await cardTitles();

      await user.type(screen.getByRole("textbox", { name: "Filter the config" }), "budg");

      expect(await cardTitles()).toEqual(["budgets"]);
      expect(screen.getByText("maxCostUSD")).toBeInTheDocument();
      expect(screen.getByText("note")).toBeInTheDocument();
    });

    it("narrows the runtime cards too", async () => {
      const user = userEvent.setup();
      openSettings();
      await cardTitles();

      await user.type(screen.getByRole("textbox", { name: "Filter the config" }), "latency");

      expect(await screen.findByRole("region", { name: "Routes" })).toBeInTheDocument();
      expect(screen.queryByRole("region", { name: "Listeners" })).not.toBeInTheDocument();
      expect(screen.queryByRole("heading", { name: "File settings" })).not.toBeInTheDocument();
    });

    it("survives a reload, because it is in the address", async () => {
      openSettings("/settings?q=kubernetes");

      expect(await cardTitles()).toEqual(["sources"]);
      expect(screen.getByRole("textbox", { name: "Filter the config" })).toHaveValue("kubernetes");
    });

    it("takes the text as plain text, not as a pattern", async () => {
      openSettings("/settings?q=kube.*");

      expect(await screen.findByRole("heading", { name: "Nothing matches" })).toBeInTheDocument();
      expect(screen.getByRole("status")).toHaveTextContent("0 of 7 sections match.");
    });

    it("shows every card again when it is cleared", async () => {
      const user = userEvent.setup();
      openSettings("/settings?q=kubernetes");
      await cardTitles();

      await user.clear(screen.getByRole("textbox", { name: "Filter the config" }));

      expect(await cardTitles()).toEqual(FILE_KEYS);
      expect(document.querySelectorAll("details[open]")).toHaveLength(0);
    });
  });

  describe("states", () => {
    it("says that it is loading", async () => {
      const { handler } = recording(() => new Promise<Response>(() => undefined));
      renderApp({ me: ADMIN, route: "/settings", handler });

      expect(await screen.findByRole("status")).toHaveTextContent("Loading");
    });

    it("shows an error with a way to try again", async () => {
      const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      const { handler } = recording(() => problem(500, "The database is down."));
      renderApp({ me: ADMIN, route: "/settings", handler, queryClient });

      expect(await screen.findByRole("heading", { name: "Could not load this page" })).toBeInTheDocument();
      expect(screen.getByText("The database is down.")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
    });

    it("names the role when the API answers 403", async () => {
      const { handler } = recording(() => problem(403, "This needs the admin role."));
      renderApp({ me: ADMIN, route: "/settings", handler });

      expect(await screen.findByRole("heading", { name: "Needs the admin role" })).toBeInTheDocument();
    });

    it("shows a role below admin the role message and sends no request for the config", async () => {
      const { handler, seen } = recording(() => json(200, sampleConfig()));
      renderApp({ me: VIEWER, route: "/settings", handler });

      expect(await screen.findByRole("heading", { name: "Needs the admin role" })).toBeInTheDocument();
      expect(seen).toEqual([]);
    });
  });
});
