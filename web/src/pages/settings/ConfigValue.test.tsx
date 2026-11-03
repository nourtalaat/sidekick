import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ConfigValue } from "./ConfigValue";

function renderValue(value: unknown) {
  return render(<ConfigValue value={value} label="field" />);
}

describe("ConfigValue", () => {
  it("shows a map as a definition list, keys in the order given", () => {
    const { container } = renderValue({ provider: "anthropic", timeout: "10m0s", nested: { depth: 2 } });

    expect(Array.from(container.querySelectorAll("dt")).map((dt) => dt.textContent)).toEqual(["provider", "timeout", "nested", "depth"]);
    expect(screen.getByText("10m0s")).toBeInTheDocument();
    expect(screen.getByText("2")).toBeInTheDocument();
  });

  it("shows a list as bullets", () => {
    renderValue(["payments", "checkout"]);

    expect(screen.getAllByRole("listitem").map((li) => li.textContent)).toEqual(["payments", "checkout"]);
  });

  it.each([
    { name: "true", value: true, text: "true" },
    { name: "false", value: false, text: "false" },
    { name: "a number", value: 12.5, text: "12.5" },
    { name: "null", value: null, text: "Not set" },
    { name: "an empty text", value: "", text: "Empty text" },
    { name: "an empty list", value: [], text: "Empty list" },
    { name: "an empty map", value: {}, text: "Empty" },
  ])("shows $name as $text", ({ value, text }) => {
    renderValue(value);

    expect(screen.getByText(text)).toBeInTheDocument();
  });

  it("shows text from the server as text, never as HTML", () => {
    const { container } = renderValue("<b>bold</b> & <img src=x onerror=alert(1)>");

    expect(screen.getByText("<b>bold</b> & <img src=x onerror=alert(1)>")).toBeInTheDocument();
    expect(container.querySelector("b, img")).toBeNull();
  });

  it("puts a long text in a block that takes focus and has a name", () => {
    renderValue("x".repeat(200));

    const block = screen.getByRole("group", { name: "Value of field" });
    expect(block).toHaveAttribute("tabindex", "0");
    expect(block).toHaveTextContent("x".repeat(200));
  });

  it("puts a text with a line break in a block", () => {
    renderValue("first\nsecond");

    expect(screen.getByRole("group", { name: "Value of field" })).toBeInTheDocument();
  });

  it("keeps a short text out of a block", () => {
    renderValue("short");

    expect(screen.queryByRole("group")).not.toBeInTheDocument();
  });

  describe("a reference to a Secret", () => {
    it("shows its name and key and nothing else, with no button and no field", () => {
      const { container } = renderValue({ name: "sidekick-db", key: "url" });

      expect(container).toHaveTextContent(/^Secret sidekick-db, key url$/);
      expect(screen.queryByRole("button")).not.toBeInTheDocument();
      expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    });

    it("says that no secret is set when both are empty", () => {
      renderValue({ name: "", key: "" });

      expect(screen.getByText("No secret set")).toBeInTheDocument();
    });

    it("is found inside a list too", () => {
      const { container } = renderValue([{ name: "a", key: "b" }]);

      expect(container).toHaveTextContent("Secret a, key b");
    });

    it("is a plain map when it has any other key", () => {
      const { container } = renderValue({ name: "db", key: "url", extra: "shown" });

      expect(container.querySelectorAll("dt")).toHaveLength(3);
      expect(container).not.toHaveTextContent("Secret db");
    });
  });
});
