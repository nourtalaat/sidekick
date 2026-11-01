import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router";
import { describe, expect, it } from "vitest";
import { Tabs, type TabDef } from "./Tabs";

const TABS: readonly TabDef[] = [
  { id: "one", label: "One", Panel: () => <p>First panel</p> },
  { id: "two", label: "Two", Panel: () => <p>Second panel</p> },
  { id: "three", label: "Three", Panel: () => <p>Third panel</p> },
];

function Address() {
  const location = useLocation();

  return <output aria-label="address">{location.search}</output>;
}

function renderTabs(route: string) {
  return render(
    <MemoryRouter initialEntries={[route]}>
      <Tabs tabs={TABS} label="Things" idPrefix="things" />
      <Address />
    </MemoryRouter>,
  );
}

describe("Tabs", () => {
  it("opens on the first tab when the address names none or an unknown one", () => {
    for (const route of ["/", "/?tab=nonsense"]) {
      const { unmount } = renderTabs(route);

      expect(screen.getByRole("tab", { name: "One" })).toHaveAttribute("aria-selected", "true");
      expect(screen.getByText("First panel")).toBeInTheDocument();

      unmount();
    }
  });

  it("opens on the tab the address names, and names the panel after its tab", () => {
    renderTabs("/?tab=two");

    expect(screen.getByRole("tab", { name: "Two" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tabpanel")).toHaveAccessibleName("Two");
    expect(screen.getByRole("tabpanel")).toHaveAttribute("id", "things-panel-two");
  });

  it("keeps the selected tab in the address and drops the other parameters", async () => {
    renderTabs("/?tab=one&q=pool");

    await userEvent.click(screen.getByRole("tab", { name: "Three" }));

    expect(screen.getByLabelText("address")).toHaveTextContent("?tab=three");
    expect(screen.getByText("Third panel")).toBeInTheDocument();
  });

  it("moves between the tabs with the arrow keys, Home and End", async () => {
    renderTabs("/");

    screen.getByRole("tab", { name: "One" }).focus();

    await userEvent.keyboard("{ArrowLeft}");
    expect(screen.getByRole("tab", { name: "Three" })).toHaveFocus();
    expect(screen.getByRole("tab", { name: "Three" })).toHaveAttribute("aria-selected", "true");

    await userEvent.keyboard("{ArrowRight}");
    expect(screen.getByRole("tab", { name: "One" })).toHaveFocus();

    await userEvent.keyboard("{End}");
    expect(screen.getByRole("tab", { name: "Three" })).toHaveFocus();

    await userEvent.keyboard("{Home}");
    expect(screen.getByRole("tab", { name: "One" })).toHaveFocus();
  });

  it("has one stop in the tab order", () => {
    renderTabs("/?tab=two");

    expect(screen.getByRole("tab", { name: "Two" })).toHaveAttribute("tabindex", "0");
    expect(screen.getByRole("tab", { name: "One" })).toHaveAttribute("tabindex", "-1");
  });
});
