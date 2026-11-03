import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { UsedMemory as UsedMemoryExample } from "../../api/investigations";
import { renderWithProviders, fakeServer, VIEWER } from "../../test/helpers";
import { ADMIN, RESPONDER } from "../../test/rules";
import { UsedMemory } from "./UsedMemory";

const ID = "0192d3a4-0000-7000-8000-0000000000e1";

function example(over: Partial<UsedMemoryExample> = {}): UsedMemoryExample {
  return {
    investigationId: ID,
    title: "Checkout p99 latency above 2s",
    verdict: "true_positive",
    summary: "p99 latency rose after a deploy.",
    cause: "Deploy 4f2c1a",
    match: "fingerprint",
    rank: 1,
    present: true,
    disabled: false,
    ...over,
  };
}

function show(examples: readonly UsedMemoryExample[] | undefined, me = VIEWER) {
  return renderWithProviders(<UsedMemory examples={examples} />, { api: fakeServer(me).api });
}

describe("the memory used card", () => {
  it.each([
    ["fingerprint", "Chosen for the same alert fingerprint"],
    ["service_alert", "Chosen for the same service and alert name"],
    ["text", "Chosen for similar words"],
    ["similar", "Chosen for a similar description"],
  ] as const)("says why an example with the match %s was chosen", (match, words) => {
    show([example({ match })]);

    expect(screen.getByText(words)).toBeInTheDocument();
  });

  it("shows the verdict, links the title to the investigation and says once that the text is current", () => {
    show([example(), example({ investigationId: "other", title: undefined, verdict: "false_positive", rank: 2 })]);

    const card = screen.getByRole("region", { name: "Memory used" });

    expect(within(card).getByText("Real issue")).toBeInTheDocument();
    expect(within(card).getByText("False alarm")).toBeInTheDocument();
    expect(within(card).getByRole("link", { name: "Checkout p99 latency above 2s" })).toHaveAttribute("href", `/investigations/${ID}`);
    expect(within(card).getByRole("link", { name: "p99 latency rose after a deploy." })).toHaveAttribute("href", "/investigations/other");
    expect(within(card).getAllByText("These are the entries as they are now; the prompt had their text at the time.")).toHaveLength(1);
  });

  it("names an entry that is gone and still links it", () => {
    show([example({ present: false, verdict: undefined, summary: undefined, cause: undefined })]);

    const item = screen.getByRole("listitem");

    expect(within(item).getByText("No longer in memory")).toBeInTheDocument();
    expect(within(item).getByRole("link", { name: "Checkout p99 latency above 2s" })).toHaveAttribute("href", `/investigations/${ID}`);
    expect(within(item).queryByText("Real issue")).not.toBeInTheDocument();
  });

  it("names the investigation by its ID when nothing else is left of it", () => {
    show([example({ present: false, title: undefined, verdict: undefined, summary: undefined })]);

    expect(screen.getByRole("link", { name: "Investigation 0192d3a4" })).toHaveAttribute("href", `/investigations/${ID}`);
  });

  it("says that an entry was disabled", () => {
    show([example({ disabled: true })]);

    expect(screen.getByText("Disabled since this run")).toBeInTheDocument();
    expect(screen.queryByText("No longer in memory")).not.toBeInTheDocument();
  });

  it("says nothing about the state of an entry that is as it was", () => {
    show([example()]);

    expect(screen.queryByText("Disabled since this run")).not.toBeInTheDocument();
    expect(screen.queryByText("No longer in memory")).not.toBeInTheDocument();
  });

  it("keeps a long summary whole in the page and cuts it to a few lines", () => {
    const long = "word ".repeat(400).trim();

    show([example({ title: undefined, summary: long })]);

    const link = screen.getByRole("link", { name: long });

    expect(link).toHaveClass("sk-memory__link");
  });

  it("shows text with markup as plain text", () => {
    const { container } = show([example({ title: "<b>Checkout</b> <script>x()</script>" })]);

    expect(screen.getByRole("link", { name: "<b>Checkout</b> <script>x()</script>" })).toBeInTheDocument();
    expect(container.querySelector("b, script")).toBeNull();
  });

  it.each([
    ["an admin", ADMIN, true],
    ["a responder", RESPONDER, false],
    ["a viewer", VIEWER, false],
  ] as const)("shows the link to the memory page to %s: %s", (_name, me, shown) => {
    show([example()], me);

    const link = screen.queryByRole("link", { name: "Manage memory" });

    if (shown) {
      expect(link).toHaveAttribute("href", "/lessons?tab=memory");
    } else {
      expect(link).not.toBeInTheDocument();
    }
  });

  it.each([[undefined], [[]]])("is not shown for no examples (%j)", (examples) => {
    show(examples, ADMIN);

    expect(screen.queryByRole("region", { name: "Memory used" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Manage memory" })).not.toBeInTheDocument();
  });
});
