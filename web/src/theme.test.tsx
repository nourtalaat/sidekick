import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { ThemeSwitch } from "./shell/ThemeSwitch";
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
