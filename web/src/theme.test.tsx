import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { ThemeSwitch } from "./shell/ThemeSwitch";
import { json, problem, renderApp, VIEWER, type Handler } from "./test/helpers";
import { THEME_STORAGE_KEY, ThemeProvider } from "./theme";

function renderSwitch() {
  return render(
    <ThemeProvider>
      <ThemeSwitch />
    </ThemeProvider>,
  );
}

describe("the theme switch", () => {
  afterEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute("data-theme");
  });

  it("follows the system until a choice is made", () => {
    renderSwitch();

    expect(screen.getByRole("radio", { name: "System" })).toBeChecked();
    expect(document.documentElement).not.toHaveAttribute("data-theme");
  });

  it.each([
    ["Light", "light"],
    ["Dark", "dark"],
  ])("sets the %s theme and keeps the choice", async (label, value) => {
    const user = userEvent.setup();
    renderSwitch();

    await user.click(screen.getByRole("radio", { name: label }));

    expect(screen.getByRole("radio", { name: label })).toBeChecked();
    expect(document.documentElement).toHaveAttribute("data-theme", value);
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe(value);
  });

  it("goes back to following the system", async () => {
    const user = userEvent.setup();
    renderSwitch();

    await user.click(screen.getByRole("radio", { name: "Dark" }));
    await user.click(screen.getByRole("radio", { name: "System" }));

    expect(document.documentElement).not.toHaveAttribute("data-theme");
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe("system");
  });

  it("restores the stored choice", () => {
    localStorage.setItem(THEME_STORAGE_KEY, "dark");
    renderSwitch();

    expect(screen.getByRole("radio", { name: "Dark" })).toBeChecked();
    expect(document.documentElement).toHaveAttribute("data-theme", "dark");
  });

  it("ignores a stored value that is not a theme", () => {
    localStorage.setItem(THEME_STORAGE_KEY, "purple");
    renderSwitch();

    expect(screen.getByRole("radio", { name: "System" })).toBeChecked();
  });
});

const PAGE_ROUTE = "/benchmarks";
const pageHandler: Handler = () => json(200, { runs: [] });

/** A fake save of the theme that keeps each body it gets and answers with reply. */
function recordSaves(reply: Handler = () => new Response(null, { status: 204 })) {
  const saved: unknown[] = [];

  const saveTheme: Handler = async (request) => {
    saved.push(await request.clone().json());

    return reply(request);
  };

  return { saved, saveTheme };
}

async function renderSignedIn(options: { theme?: "system" | "light" | "dark"; saveTheme?: Handler } = {}) {
  const result = renderApp({ me: { ...VIEWER, theme: options.theme }, route: PAGE_ROUTE, handler: pageHandler, saveTheme: options.saveTheme });
  await screen.findByRole("heading", { name: "Benchmarks", level: 1 });

  return result;
}

describe("the theme saved per user", () => {
  afterEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute("data-theme");
  });

  it("takes the choice of the server over the one this browser kept, and does not send it back", async () => {
    localStorage.setItem(THEME_STORAGE_KEY, "light");
    const { saved, saveTheme } = recordSaves();

    await renderSignedIn({ theme: "dark", saveTheme });

    expect(screen.getByRole("radio", { name: "Dark" })).toBeChecked();
    expect(document.documentElement).toHaveAttribute("data-theme", "dark");
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe("dark");
    expect(saved).toEqual([]);
  });

  it("sends the choice this browser kept once, when the server has none", async () => {
    localStorage.setItem(THEME_STORAGE_KEY, "dark");
    const { saved, saveTheme } = recordSaves();

    await renderSignedIn({ saveTheme });

    await waitFor(() => expect(saved).toEqual([{ theme: "dark" }]));
    expect(screen.getByRole("radio", { name: "Dark" })).toBeChecked();
    expect(document.documentElement).toHaveAttribute("data-theme", "dark");
  });

  it("sends nothing when neither the server nor this browser has a choice", async () => {
    const { saved, saveTheme } = recordSaves();

    await renderSignedIn({ saveTheme });

    expect(screen.getByRole("radio", { name: "System" })).toBeChecked();
    expect(saved).toEqual([]);
  });

  it("applies a change at once, keeps it in the browser and sends it with the CSRF token", async () => {
    const user = userEvent.setup();
    const { saved, saveTheme } = recordSaves();
    const { seen } = await renderSignedIn({ theme: "dark", saveTheme });

    await user.click(screen.getByRole("radio", { name: "Light" }));

    expect(screen.getByRole("radio", { name: "Light" })).toBeChecked();
    expect(document.documentElement).toHaveAttribute("data-theme", "light");
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe("light");
    await waitFor(() => expect(saved).toEqual([{ theme: "light" }]));

    const put = seen.find((request) => request.method === "PUT" && request.path === "/api/v1/me/theme");
    expect(put?.headers.get("X-CSRF-Token")).toBe("csrf-ada");
  });

  it.each<[string, Handler]>([
    ["the server refuses it", () => problem(500, "Something went wrong.")],
    [
      "the server cannot be reached",
      () => {
        throw new TypeError("Failed to fetch");
      },
    ],
  ])("keeps the screen as chosen when %s, and sends the next change", async (_name, fail) => {
    const user = userEvent.setup();
    let failing = true;
    const { saved, saveTheme } = recordSaves((request) => (failing ? fail(request) : new Response(null, { status: 204 })));
    await renderSignedIn({ theme: "system", saveTheme });

    await user.click(screen.getByRole("radio", { name: "Dark" }));
    await waitFor(() => expect(saved).toEqual([{ theme: "dark" }]));

    expect(screen.getByRole("radio", { name: "Dark" })).toBeChecked();
    expect(document.documentElement).toHaveAttribute("data-theme", "dark");
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe("dark");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    failing = false;
    await user.click(screen.getByRole("radio", { name: "Light" }));
    await waitFor(() => expect(saved).toEqual([{ theme: "dark" }, { theme: "light" }]));

    expect(screen.getByRole("radio", { name: "Light" })).toBeChecked();
  });

  it("uses the choice this browser kept on the login page, which has no session", async () => {
    localStorage.setItem(THEME_STORAGE_KEY, "dark");
    const { seen } = renderApp({ me: null, route: "/login" });

    expect(await screen.findByRole("link", { name: "Sign in" })).toBeInTheDocument();
    expect(document.documentElement).toHaveAttribute("data-theme", "dark");
    expect(seen.filter((request) => request.method === "PUT")).toEqual([]);
  });

  it("follows the system on the login page when this browser has no choice", async () => {
    renderApp({ me: null, route: "/login" });

    expect(await screen.findByRole("link", { name: "Sign in" })).toBeInTheDocument();
    expect(document.documentElement).not.toHaveAttribute("data-theme");
  });
});
