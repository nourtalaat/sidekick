import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { json, problem, renderApp, wcagViolations } from "../../test/helpers";
import { ADMIN, RESPONDER, codedProblem, recording, rowAt, validationProblem } from "../../test/rules";
import { scenario, scenarioDetail } from "../../test/scenarios";

const ID = "latency-alert-checkout-0198f2";
const DETAIL_PATH = `/api/v1/scenarios/${ID}`;

describe("the scenario list", () => {
  it("asks for the admin role and loads nothing for anyone below it", async () => {
    const { handler, seen } = recording(() => json(200, { scenarios: [] }));
    renderApp({ me: RESPONDER, route: "/review?tab=scenarios", handler });

    expect(await screen.findByRole("heading", { name: "Needs the admin role" })).toBeInTheDocument();
    expect(seen.some((r) => r.path === "/api/v1/scenarios")).toBe(false);
  });

  it("lists the scenarios with their status, a link to each and to its investigation", async () => {
    const { handler } = recording(() =>
      json(200, {
        scenarios: [scenario(), scenario({ id: "other-1", title: "Other", status: "reviewed", reviewedBy: "Ad", investigationId: undefined })],
      }),
    );

    renderApp({ me: ADMIN, route: "/review?tab=scenarios", handler });

    const table = await screen.findByRole("region", { name: "Scenarios" });
    const rows = within(table).getAllByRole("row");

    expect(rows).toHaveLength(3);
    expect(within(rowAt(rows, 1)).getByText("Draft")).toBeInTheDocument();
    expect(within(rowAt(rows, 1)).getByRole("link", { name: "Checkout latency above 2s" })).toHaveAttribute("href", `/review?tab=scenarios&id=${ID}`);
    expect(within(rowAt(rows, 1)).getByRole("link", { name: "Open investigation" })).toHaveAttribute("href", "/investigations/0192d3a4-0000-7000-8000-0000000000c1");
    expect(within(rowAt(rows, 2)).getByText("Reviewed")).toBeInTheDocument();
    expect(within(rowAt(rows, 2)).getByText("Deleted")).toBeInTheDocument();
  });

  it("filters by status through the address", async () => {
    const user = userEvent.setup();
    const { handler, seen } = recording(() => json(200, { scenarios: [scenario()] }));

    renderApp({ me: ADMIN, route: "/review?tab=scenarios", handler });
    await screen.findByRole("region", { name: "Scenarios" });

    await user.selectOptions(screen.getByLabelText("Show"), "reviewed");

    await waitFor(() => expect(seen.some((r) => r.search.get("status") === "reviewed")).toBe(true));
    expect(screen.getByLabelText("Show")).toHaveValue("reviewed");
  });

  it("shows more with the cursor of the page before", async () => {
    const user = userEvent.setup();

    const { handler, seen } = recording((request) =>
      request.search.get("cursor") === "next-1"
        ? json(200, { scenarios: [scenario({ id: "second", title: "Second one" })] })
        : json(200, { scenarios: [scenario()], nextCursor: "next-1" }),
    );

    renderApp({ me: ADMIN, route: "/review?tab=scenarios", handler });

    await user.click(await screen.findByRole("button", { name: "Show more" }));

    expect(await screen.findByRole("link", { name: "Second one" })).toBeInTheDocument();
    expect(seen.filter((r) => r.path === "/api/v1/scenarios")).toHaveLength(2);
  });
});

describe("a scenario to review", () => {
  it("puts each part next to its original and marks what was replaced and what was kept", async () => {
    const { handler } = recording(() => json(200, scenarioDetail()));
    const { container } = renderApp({ me: ADMIN, route: `/review?tab=scenarios&id=${ID}`, handler });

    expect(await screen.findByRole("heading", { name: "Checkout latency above 2s", level: 2 })).toBeInTheDocument();
    expect(screen.getByText("Draft")).toBeInTheDocument();

    const payload = screen.getByRole("region", { name: "Event payload" });
    expect(within(payload).getByText('{"instance": "web-7.prod.acme.com"}')).toBeInTheDocument();
    expect(within(payload).getByText("host-1").tagName).toBe("MARK");

    // The cleaner kept this trace ID and says so.
    const answer = screen.getByRole("region", { name: "signoz search_logs: answer 1" });
    const kept = within(answer).getByText("tokXYZ12345678");
    expect(kept.tagName).toBe("MARK");
    expect(kept).toHaveAttribute("title", "looks like an opaque ID");

    // An original that is gone says why.
    const gone = screen.getByRole("region", { name: "github list_commits: answer 1" });
    expect(within(gone).getByText("The original is no longer kept.")).toBeInTheDocument();

    expect(screen.getByText("k8s was unavailable in the investigation.")).toBeInTheDocument();
    expect(screen.getByText(/sources.signoz.fixtures\[0\].respond.text/)).toBeInTheDocument();

    expect(await wcagViolations(container)).toEqual([]);
  });

  it("marks it as reviewed only after the tick, with the revision that was read", async () => {
    const user = userEvent.setup();
    let reviewed = false;

    const { handler, seen } = recording((request) => {
      if (request.method === "POST") {
        reviewed = true;

        return json(200, scenario({ status: "reviewed", reviewedBy: "Ad", reviewedAt: "2026-10-30T10:00:00Z" }));
      }

      return json(200, scenarioDetail({ scenario: reviewed ? scenario({ status: "reviewed", reviewedBy: "Ad", reviewedAt: "2026-10-30T10:00:00Z" }) : scenario({ revision: 3 }) }));
    });

    renderApp({ me: ADMIN, route: `/review?tab=scenarios&id=${ID}`, handler });

    const button = await screen.findByRole("button", { name: "Mark as reviewed" });
    expect(button).toBeDisabled();

    await user.click(screen.getByRole("checkbox", { name: "I checked this for sensitive data" }));
    expect(button).toBeEnabled();

    await user.click(button);

    expect(await screen.findByText(/Reviewed by Ad/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Download" })).toHaveAttribute("href", `${DETAIL_PATH}/file`);

    const post = seen.find((r) => r.method === "POST");
    expect(post?.path).toBe(`${DETAIL_PATH}/review`);
    expect(post?.body).toEqual({ revision: 3, sensitiveDataChecked: true });
    expect(post?.csrf).toBe(ADMIN.csrfToken);
  });

  it("says when the scenario changed since it was read", async () => {
    const user = userEvent.setup();

    const { handler } = recording((request) =>
      request.method === "POST" ? codedProblem(409, "stale", "The scenario changed since you read it. Reload it to see the new version.") : json(200, scenarioDetail()),
    );

    renderApp({ me: ADMIN, route: `/review?tab=scenarios&id=${ID}`, handler });

    await user.click(await screen.findByRole("checkbox", { name: "I checked this for sensitive data" }));
    await user.click(screen.getByRole("button", { name: "Mark as reviewed" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Could not mark it as reviewed");
    expect(alert).toHaveTextContent("The scenario changed since you read it.");
  });

  it("saves an edit with the revision, and holds the review back until it is saved", async () => {
    const user = userEvent.setup();
    const { handler, seen } = recording((request) => (request.method === "PUT" ? json(200, scenario({ revision: 2 })) : json(200, scenarioDetail())));

    renderApp({ me: ADMIN, route: `/review?tab=scenarios&id=${ID}`, handler });

    await user.click(await screen.findByRole("button", { name: "Edit the scenario file" }));

    const editor = screen.getByLabelText("Scenario file (YAML)");
    await user.type(editor, "# note");

    expect(screen.getByText("Save your edits before you review.")).toBeInTheDocument();
    await user.click(screen.getByRole("checkbox", { name: "I checked this for sensitive data" }));
    expect(screen.getByRole("button", { name: "Mark as reviewed" })).toBeDisabled();

    await user.click(screen.getByRole("button", { name: "Save the file" }));

    await waitFor(() => expect(seen.some((r) => r.method === "PUT")).toBe(true));
    const put = seen.find((r) => r.method === "PUT");
    expect(put?.path).toBe(DETAIL_PATH);
    expect(put?.body).toEqual({ revision: 1, body: expect.stringContaining("# note") });
  });

  it("lists the fields a refused edit names", async () => {
    const user = userEvent.setup();

    const { handler } = recording((request) =>
      request.method === "PUT"
        ? validationProblem([
            { field: "title", message: "still holds something that looks like an email address" },
            { field: "sources.signoz", message: "tools.snapshot: required" },
          ])
        : json(200, scenarioDetail()),
    );

    renderApp({ me: ADMIN, route: `/review?tab=scenarios&id=${ID}`, handler });

    await user.click(await screen.findByRole("button", { name: "Edit the scenario file" }));
    await user.click(screen.getByRole("button", { name: "Save the file" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("title: still holds something that looks like an email address");
    expect(alert).toHaveTextContent("sources.signoz: tools.snapshot: required");
  });

  it("says when the scenario does not exist", async () => {
    const { handler } = recording(() => problem(404, "No such scenario."));
    renderApp({ me: ADMIN, route: "/review?tab=scenarios&id=nope", handler });

    expect(await screen.findByText("No such scenario.")).toBeInTheDocument();
  });
});
