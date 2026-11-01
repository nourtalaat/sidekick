import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { VIEWER, json, renderApp } from "../test/helpers";
import { ADMIN, codedProblem, validationProblem } from "../test/rules";
import { CHECKOUT_V1, CHECKOUT_V2, GENERIC_BODY, sampleContent, strategyApi } from "../test/strategies";

/** Replaces the text in the editor with text, as a paste does. */
async function replaceText(user: ReturnType<typeof userEvent.setup>, text: string) {
  const editor = await screen.findByLabelText("Strategy (YAML)");

  await user.clear(editor);
  await user.click(editor);
  await user.paste(text);
}

describe("the strategy page of a runtime strategy", () => {
  it("shows an admin the current text in an editor, and the history of the versions", async () => {
    const { handler } = strategyApi(sampleContent());
    renderApp({ me: ADMIN, route: "/strategies/checkout-latency", handler });

    expect(await screen.findByLabelText("Strategy (YAML)")).toHaveValue(CHECKOUT_V2);
    expect(screen.getByRole("heading", { name: "checkout-latency", level: 1 })).toBeInTheDocument();

    const table = await screen.findByRole("region", { name: "Versions of checkout-latency" });
    const rows = within(table).getAllByRole("row");

    expect(rows).toHaveLength(3);
    expect(within(rows[1] as HTMLElement).getByText("Current")).toBeInTheDocument();
    expect(within(rows[1] as HTMLElement).getByText("Add the first check")).toBeInTheDocument();
    expect(within(rows[1] as HTMLElement).getByRole("link", { name: "Changes in version 2" })).toHaveAttribute(
      "href",
      "/strategies/checkout-latency/diff?from=1&to=2",
    );
    expect(within(rows[2] as HTMLElement).queryByRole("link", { name: /Changes/ })).not.toBeInTheDocument();
  });

  it("saves a new version with the reason and the version that was read, then says which version it is", async () => {
    const user = userEvent.setup();
    const { handler, seen } = strategyApi(sampleContent());
    renderApp({ me: ADMIN, route: "/strategies/checkout-latency", handler });

    await replaceText(user, `${CHECKOUT_V2}budgets:\n  maxToolCalls: 20\n`);
    await user.type(screen.getByLabelText("Reason for this version"), "Cap the tool calls");
    await user.click(screen.getByRole("button", { name: "Save as a new version" }));

    expect(await screen.findByText("Saved version 3.")).toBeInTheDocument();

    const post = seen.find((r) => r.method === "POST");
    expect(post?.path).toBe("/api/v1/strategies/checkout-latency/versions");
    expect(post?.csrf).toBe(VIEWER.csrfToken);
    expect(post?.body).toEqual({ body: `${CHECKOUT_V2}budgets:\n  maxToolCalls: 20\n`, reason: "Cap the tool calls", baseVersion: 2 });

    expect(screen.getByLabelText("Reason for this version")).toHaveValue("");
    expect(screen.getByLabelText("Strategy (YAML)")).toHaveValue(`${CHECKOUT_V2}budgets:\n  maxToolCalls: 20\n`);
  });

  it("lists each problem with its path and line, and shows the reason problem under its field", async () => {
    const user = userEvent.setup();
    const content = sampleContent();

    content.save = () =>
      validationProblem([
        { field: "reason", message: "a reason is required" },
        { field: "body.match.textPattern", message: "invalid regular expression (line 4)" },
        { field: "body", message: "line 9: unknown key \"colour\"" },
      ]);

    const { handler } = strategyApi(content);
    renderApp({ me: ADMIN, route: "/strategies/checkout-latency", handler });

    await replaceText(user, `${CHECKOUT_V2}x`);
    await user.click(screen.getByRole("button", { name: "Save as a new version" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveFocus();
    expect(within(alert).getByText("Not saved. The strategy has problems")).toBeInTheDocument();
    expect(within(alert).getByText("match.textPattern: invalid regular expression (line 4)")).toBeInTheDocument();
    expect(within(alert).getByText('line 9: unknown key "colour"')).toBeInTheDocument();
    expect(screen.getByLabelText("Reason for this version")).toHaveAccessibleDescription(expect.stringContaining("a reason is required"));
    expect(screen.getByLabelText("Strategy (YAML)")).toHaveValue(`${CHECKOUT_V2}x`);
  });

  it("says when someone saved a newer version, and loads it on request", async () => {
    const user = userEvent.setup();
    const content = sampleContent();
    content.save = () => codedProblem(409, "stale", "The strategy checkout-latency was saved again since you read it.");

    const { handler, seen } = strategyApi(content);
    renderApp({ me: ADMIN, route: "/strategies/checkout-latency", handler });

    await replaceText(user, `${CHECKOUT_V2}x`);
    await user.type(screen.getByLabelText("Reason for this version"), "Late");
    await user.click(screen.getByRole("button", { name: "Save as a new version" }));

    expect(await screen.findByText("Someone saved a newer version")).toBeInTheDocument();
    expect(seen.filter((r) => r.method === "POST")).toHaveLength(1);

    await user.click(screen.getByRole("button", { name: "Load the newest version" }));

    await waitFor(() => expect(screen.getByLabelText("Strategy (YAML)")).toHaveValue(CHECKOUT_V2));
    expect(screen.queryByText("Someone saved a newer version")).not.toBeInTheDocument();
  });

  it("says when the text is the same as the current version", async () => {
    const user = userEvent.setup();
    const content = sampleContent();
    content.save = () =>
      json(422, {
        type: "about:blank",
        title: "Unprocessable Entity",
        status: 422,
        detail: "The strategy is the same as its current version, so there is nothing to save.",
        code: "unchanged",
        errors: [{ field: "body", message: "The strategy is the same as its current version." }],
      });

    const { handler } = strategyApi(content);
    renderApp({ me: ADMIN, route: "/strategies/checkout-latency", handler });

    await screen.findByLabelText("Strategy (YAML)");
    await user.type(screen.getByLabelText("Reason for this version"), "Again");
    await user.click(screen.getByRole("button", { name: "Save as a new version" }));

    expect(await screen.findByText("Nothing to save")).toBeInTheDocument();
  });

  it("shows what the admin changed, and resets the text", async () => {
    const user = userEvent.setup();
    const { handler } = strategyApi(sampleContent());
    renderApp({ me: ADMIN, route: "/strategies/checkout-latency", handler });

    await screen.findByLabelText("Strategy (YAML)");

    expect(screen.getByRole("button", { name: "Show my changes" })).toBeDisabled();

    await replaceText(user, `${CHECKOUT_V2}budgets: {}\n`);
    await user.click(screen.getByRole("button", { name: "Show my changes" }));

    const diff = screen.getByRole("region", { name: "Your changes" });
    expect(within(diff).getByText("Added:")).toBeInTheDocument();
    expect(screen.getByText("1 line added, 0 lines removed.")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Reset" }));

    expect(screen.getByLabelText("Strategy (YAML)")).toHaveValue(CHECKOUT_V2);
    expect(screen.queryByRole("region", { name: "Your changes" })).not.toBeInTheDocument();
  });

  it("starts the editor from an older version when asked", async () => {
    const { handler, seen } = strategyApi(sampleContent());
    renderApp({ me: ADMIN, route: "/strategies/checkout-latency?base=1", handler });

    expect(await screen.findByText("This starts from version 1. Saving it adds a new version; the older ones stay in the history.")).toBeInTheDocument();
    expect(await screen.findByLabelText("Strategy (YAML)")).toHaveValue(CHECKOUT_V1);
    expect(seen.some((r) => r.path.endsWith("/versions/1"))).toBe(true);
  });

  it("shows a viewer the text and no editor", async () => {
    const { handler } = strategyApi(sampleContent());
    renderApp({ me: VIEWER, route: "/strategies/checkout-latency", handler });

    expect(await screen.findByLabelText("Strategy YAML")).toHaveTextContent("Check what changed.");
    expect(screen.queryByLabelText("Strategy (YAML)")).not.toBeInTheDocument();
    expect(screen.getByText("Changing strategies needs the admin role.")).toBeInTheDocument();
    expect(await screen.findByRole("region", { name: "Versions of checkout-latency" })).toBeInTheDocument();
  });

  it("says when no strategy has the name", async () => {
    const { handler } = strategyApi(sampleContent());
    renderApp({ me: ADMIN, route: "/strategies/missing", handler });

    expect(await screen.findByText("No strategy has this name")).toBeInTheDocument();
  });
});

describe("the strategy page of a built-in strategy", () => {
  it("shows the text read-only, and starts a runtime strategy from it", async () => {
    const user = userEvent.setup();
    const { handler, seen } = strategyApi(sampleContent());
    renderApp({ me: ADMIN, route: "/strategies/generic-alert", handler });

    expect(await screen.findByLabelText("Strategy YAML")).toHaveTextContent("Read the alert.");
    expect(screen.queryByLabelText("Strategy (YAML)")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Edit as runtime strategy" }));

    expect(screen.getByLabelText("Strategy (YAML)")).toHaveValue(GENERIC_BODY);
    expect(screen.queryByLabelText("Strategy YAML")).not.toBeInTheDocument();

    await user.type(screen.getByLabelText("Reason for this version"), "Our own first checks");
    await user.click(screen.getByRole("button", { name: "Save as a new version" }));

    expect(await screen.findByText("Saved version 1.")).toBeInTheDocument();
    expect(seen.find((r) => r.method === "POST")?.body).toEqual({ body: GENERIC_BODY, reason: "Our own first checks", baseVersion: 0 });
  });

  it("gives a viewer no way to edit it", async () => {
    const { handler } = strategyApi(sampleContent());
    renderApp({ me: VIEWER, route: "/strategies/generic-alert", handler });

    expect(await screen.findByLabelText("Strategy YAML")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit as runtime strategy" })).not.toBeInTheDocument();
  });
});

describe("the strategy page of a strategy file", () => {
  it("is read-only, and tells that the file wins", async () => {
    const { handler } = strategyApi(sampleContent());
    renderApp({ me: ADMIN, route: "/strategies/file-latency", handler });

    expect(await screen.findByText("This is a strategy file")).toBeInTheDocument();
    expect(screen.getByLabelText("Strategy YAML")).toHaveTextContent("From a file.");
    expect(screen.queryByRole("button", { name: "Edit as runtime strategy" })).not.toBeInTheDocument();
  });
});
