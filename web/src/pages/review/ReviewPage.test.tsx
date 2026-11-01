import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { VIEWER, json, renderApp, wcagViolations } from "../../test/helpers";
import { APPROVER, proposal } from "../../test/proposals";
import { recording } from "../../test/rules";

/** A handler that serves the proposals for the statuses asked. */
function queue() {
  return recording((request) => {
    if (request.path === "/api/v1/proposals") {
      const decided = request.search.getAll("status").includes("approved");

      return json(200, {
        proposals: decided
          ? [proposal({ id: "0192d3a4-0000-7000-8000-0000000000f4", action: "ticket-comment", status: "succeeded", decidedByName: "Ana" })]
          : [proposal(), proposal({ id: "0192d3a4-0000-7000-8000-0000000000f2", action: "slack-react" })],
      });
    }

    return json(404, {});
  });
}

describe("the review queue", () => {
  it("lists the actions that wait for a decision on the actions tab", async () => {
    const { handler, seen } = queue();
    renderApp({ me: APPROVER, route: "/review?tab=actions", handler });

    expect(await screen.findByRole("heading", { name: "Review queue", level: 1 })).toBeInTheDocument();
    expect(await screen.findByRole("article", { name: "Proposed action monitor-note" })).toBeInTheDocument();
    expect(screen.getByRole("article", { name: "Proposed action slack-react" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Actions" })).toHaveAttribute("aria-current", "page");

    const request = seen.find((r) => r.path === "/api/v1/proposals");
    expect(request?.search.getAll("status")).toEqual(["proposed"]);
  });

  it("opens on the actions tab when the address names no tab", async () => {
    const { handler } = queue();
    renderApp({ me: APPROVER, route: "/review", handler });

    expect(await screen.findByRole("article", { name: "Proposed action monitor-note" })).toBeInTheDocument();
  });

  it("shows the decided actions when asked", async () => {
    const user = userEvent.setup();
    const { handler, seen } = queue();
    renderApp({ me: APPROVER, route: "/review?tab=actions", handler });

    await user.selectOptions(await screen.findByLabelText("Show"), "decided");

    const card = await screen.findByRole("article", { name: "Proposed action ticket-comment" });
    expect(within(card).getByText("Done")).toBeInTheDocument();
    expect(within(card).queryByRole("button")).not.toBeInTheDocument();

    const last = seen.filter((r) => r.path === "/api/v1/proposals").at(-1);
    expect(last?.search.getAll("status")).toContain("succeeded");
    expect(last?.search.getAll("status")).not.toContain("proposed");
  });

  it("says when nothing waits", async () => {
    const { handler } = recording(() => json(200, { proposals: [] }));
    renderApp({ me: APPROVER, route: "/review?tab=actions", handler });

    expect(await screen.findByText("Nothing waits for a decision")).toBeInTheDocument();
  });

  it("asks for more when there is another page", async () => {
    const user = userEvent.setup();
    const { handler, seen } = recording((request) =>
      json(200, request.search.get("cursor") ? { proposals: [proposal({ id: "0192d3a4-0000-7000-8000-0000000000f9", action: "slack-react" })] } : { proposals: [proposal()], nextCursor: "abc" }),
    );
    renderApp({ me: APPROVER, route: "/review", handler });

    await user.click(await screen.findByRole("button", { name: "Show more" }));

    expect(await screen.findByRole("article", { name: "Proposed action slack-react" })).toBeInTheDocument();
    expect(seen.at(-1)?.search.get("cursor")).toBe("abc");
  });

  it("tells someone below approver that the tab is for approvers, without asking for the queue", async () => {
    const { handler, seen } = queue();
    renderApp({ me: VIEWER, route: "/review?tab=actions", handler });

    expect(await screen.findByText("Only approvers can see and decide the actions the model proposed.")).toBeInTheDocument();
    expect(seen.filter((r) => r.path === "/api/v1/proposals")).toHaveLength(0);
  });

  it("has no WCAG 2.2 AA violations", async () => {
    const { handler } = queue();
    const { container } = renderApp({ me: APPROVER, route: "/review?tab=actions", handler });

    await screen.findByRole("article", { name: "Proposed action monitor-note" });

    expect(await wcagViolations(container)).toEqual([]);
  });
});
