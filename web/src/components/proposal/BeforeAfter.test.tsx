import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { BeforeAfter } from "./BeforeAfter";

const PROPS = { title: "options.thresholds.critical", beforeLabel: "Running now", before: "2", afterLabel: "Goes back to", after: "3" };

describe("BeforeAfter", () => {
  it("shows the title and the two sides under the labels it is given", () => {
    render(<BeforeAfter {...PROPS} />);

    expect(screen.getByRole("heading", { name: "options.thresholds.critical" })).toBeInTheDocument();
    expect(screen.getByText("Running now")).toBeInTheDocument();
    expect(screen.getByText("Goes back to")).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Running now, options.thresholds.critical" })).toHaveTextContent("2");
    expect(screen.getByRole("region", { name: "Goes back to, options.thresholds.critical" })).toHaveTextContent("3");
  });

  it("gives each side a block that takes focus, so a long value can be scrolled from the keyboard", async () => {
    const user = userEvent.setup();
    render(<BeforeAfter {...PROPS} before={"line\n".repeat(200)} />);

    const before = screen.getByRole("region", { name: "Running now, options.thresholds.critical" });
    const after = screen.getByRole("region", { name: "Goes back to, options.thresholds.critical" });

    expect(before).toHaveAttribute("tabindex", "0");
    expect(after).toHaveAttribute("tabindex", "0");

    await user.tab();
    expect(before).toHaveFocus();

    await user.tab();
    expect(after).toHaveFocus();
  });

  it("shows the texts as text, never as markup", () => {
    const { container } = render(<BeforeAfter {...PROPS} before="<img src=x onerror=alert(1)>" after="<b>bold</b>" />);

    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("b")).toBeNull();
    expect(screen.getByText("<b>bold</b>")).toBeInTheDocument();
  });
});
