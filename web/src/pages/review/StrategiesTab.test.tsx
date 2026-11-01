import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { VIEWER, renderApp } from "../../test/helpers";
import { FAILED_ID, FILE_ID, PASSED_ID, failedProposal, passedProposal, proposalApi } from "../../test/proposals";
import { ADMIN, codedProblem } from "../../test/rules";

const ROUTE = "/review?tab=strategies";

/** Opens the review of the proposal for the strategy name, and returns the card. */
async function open(name: string, index = 0): Promise<HTMLElement> {
  const user = userEvent.setup();
  const cards = await screen.findAllByRole("article");
  const card = cards.filter((c) => within(c).queryByRole("heading", { name, level: 2 }))[index] as HTMLElement;

  await user.click(within(card).getByRole("button", { name: /Review/ }));
  await within(card).findByRole("region", { name: "The change" });

  return card;
}

describe("the strategies tab of the review page", () => {
  it("lists the proposals with their status", async () => {
    const { handler } = proposalApi({ proposals: [passedProposal(), failedProposal("not_enough_evidence")] });
    renderApp({ me: ADMIN, route: ROUTE, handler });

    expect(await screen.findByRole("tab", { name: "Strategies", selected: true })).toBeInTheDocument();

    const cards = await screen.findAllByRole("article");
    expect(cards).toHaveLength(2);
    expect(within(cards[0] as HTMLElement).getByText("Passed the gate")).toBeInTheDocument();
    expect(within(cards[0] as HTMLElement).getByText(/Drafted from version runtime:2 after 3 wrong ratings/)).toBeInTheDocument();
    expect(within(cards[1] as HTMLElement).getByText("Did not pass")).toBeInTheDocument();
  });

  it("says when there is nothing to review", async () => {
    const { handler } = proposalApi({ proposals: [] });
    renderApp({ me: ADMIN, route: ROUTE, handler });

    expect(await screen.findByText("No strategy changes to review")).toBeInTheDocument();
  });

  it("asks for a status when the filter changes", async () => {
    const user = userEvent.setup();
    const { handler, seen } = proposalApi({ proposals: [passedProposal(), failedProposal()] });
    renderApp({ me: ADMIN, route: ROUTE, handler });

    await screen.findAllByRole("article");
    await user.selectOptions(screen.getByLabelText("Show"), "failed");

    expect(await screen.findAllByRole("article")).toHaveLength(1);
    expect(seen.some((r) => r.path.endsWith("/strategy-proposals") && r.search.get("status") === "failed")).toBe(true);
  });

  it("shows the change, the reason, the feedback and the gate of a passed proposal", async () => {
    const { handler } = proposalApi({ proposals: [passedProposal()] });
    renderApp({ me: ADMIN, route: ROUTE, handler });

    const card = await open("checkout-latency");

    expect(within(card).getByText("Planned deploys were reported as incidents.")).toBeInTheDocument();
    expect(within(card).getByText("1 line added, 0 lines removed.")).toBeInTheDocument();
    expect(within(card).getByText("It was a planned deploy.")).toBeInTheDocument();
    expect(within(card).getByRole("link", { name: "The verdict" })).toHaveAttribute("href", "/investigations/inv-1");
    expect(within(card).getByText("The benchmark shows the change is not clearly worse")).toBeInTheDocument();
    expect(within(card).getByText("-0.005 to +0.030")).toBeInTheDocument();
    expect(within(card).getByText("-0.020")).toBeInTheDocument();

    const scores = within(card).getByRole("region", { name: "Score of each scenario" });
    expect(within(scores).getByRole("row", { name: /latency-a 0.800 0.820 \+0.020/ })).toBeInTheDocument();
  });

  it("explains a gate that could not tell, and turns approval off", async () => {
    const { handler } = proposalApi({ proposals: [failedProposal("not_enough_evidence")] });
    renderApp({ me: ADMIN, route: ROUTE, handler });

    const card = await open("checkout-latency");

    expect(within(card).getByText("The benchmark cannot tell")).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: "Approve and save as a new version" })).toBeDisabled();
    expect(within(card).getByText("Only a proposal that passed the benchmark gate can be approved.")).toBeInTheDocument();
  });

  it("applies a passed proposal and says which version it became", async () => {
    const user = userEvent.setup();
    const { handler, seen } = proposalApi({ proposals: [passedProposal()] });
    renderApp({ me: ADMIN, route: ROUTE, handler });

    const card = await open("checkout-latency");
    await user.click(within(card).getByRole("button", { name: "Approve and save as a new version" }));

    expect(await within(card).findByText("Applied as version 3")).toBeInTheDocument();
    expect(seen.some((r) => r.method === "POST" && r.path.endsWith(`/strategy-proposals/${PASSED_ID}/approve`) && r.csrf === ADMIN.csrfToken)).toBe(true);
  });

  it("gives the YAML to commit for a strategy in a file", async () => {
    const user = userEvent.setup();
    const { handler } = proposalApi({ proposals: [passedProposal({ id: FILE_ID, name: "file-latency", baseOrigin: "file" })] });
    renderApp({ me: ADMIN, route: ROUTE, handler });

    const card = await open("file-latency");
    await user.click(within(card).getByRole("button", { name: "Approve and get the YAML" }));

    expect(await within(card).findByText("Approved. Commit the new strategy file")).toBeInTheDocument();

    const link = within(card).getByRole("link", { name: "Download file-latency.yaml" });
    expect(link).toHaveAttribute("download", "file-latency.yaml");
    expect(decodeURIComponent(link.getAttribute("href") ?? "")).toContain("Ask whether a deploy was planned.");
  });

  it("says plainly when the server refuses an approval", async () => {
    const user = userEvent.setup();
    const { handler } = proposalApi({
      proposals: [passedProposal()],
      approve: () => codedProblem(409, "not_passed", "This proposal has not passed its benchmark gate."),
    });
    renderApp({ me: ADMIN, route: ROUTE, handler });

    const card = await open("checkout-latency");
    await user.click(within(card).getByRole("button", { name: "Approve and save as a new version" }));

    expect(await within(card).findByText("This proposal cannot be applied")).toBeInTheDocument();
    expect(within(card).getByText(/There is no way around the gate/)).toBeInTheDocument();
  });

  it("rejects a proposal with a reason", async () => {
    const user = userEvent.setup();
    const { handler, seen } = proposalApi({ proposals: [failedProposal()] });
    renderApp({ me: ADMIN, route: ROUTE, handler });

    const card = await open("checkout-latency");
    await user.type(within(card).getByLabelText("Reason for rejecting (optional)"), "Not now");
    await user.click(within(card).getByRole("button", { name: "Reject" }));

    expect(await within(card).findByText("This proposal is rejected. Nothing more can be done with it.")).toBeInTheDocument();
    expect(seen.find((r) => r.path.endsWith(`/strategy-proposals/${FAILED_ID}/reject`))?.body).toEqual({ reason: "Not now" });
  });

  it("edits a proposal and sends it through the gate again", async () => {
    const user = userEvent.setup();
    const { handler, seen } = proposalApi({ proposals: [failedProposal()] });
    renderApp({ me: ADMIN, route: ROUTE, handler });

    const card = await open("checkout-latency");
    await user.click(within(card).getByRole("button", { name: "Edit" }));

    const editor = within(card).getByLabelText("Strategy (YAML)");
    await user.clear(editor);
    await user.type(editor, "name: checkout-latency{Enter}description: Edited.{Enter}");
    await user.click(within(card).getByRole("button", { name: "Save and run the gate again" }));

    expect(await within(card).findByText("The benchmark gate has not started yet.")).toBeInTheDocument();
    expect(seen.find((r) => r.method === "PUT")?.body).toEqual({ body: "name: checkout-latency\ndescription: Edited.\n" });
  });

  it("lists the problems of an edit the server refuses", async () => {
    const user = userEvent.setup();
    const { handler } = proposalApi({ proposals: [failedProposal()] });
    renderApp({ me: ADMIN, route: ROUTE, handler });

    const card = await open("checkout-latency");
    await user.click(within(card).getByRole("button", { name: "Edit" }));
    await user.type(within(card).getByLabelText("Strategy (YAML)"), "nope: 1");
    await user.click(within(card).getByRole("button", { name: "Save and run the gate again" }));

    expect(await within(card).findByText("nope: unknown key")).toBeInTheDocument();
  });

  it("tells a viewer the page needs the admin role", async () => {
    const { handler } = proposalApi({ proposals: [] });
    renderApp({
      me: VIEWER,
      route: ROUTE,
      handler: (request) => {
        const url = new URL(request.url);

        return url.pathname.endsWith("/strategy-proposals")
          ? new Response(JSON.stringify({ status: 403, detail: "This needs the admin role." }), { status: 403, headers: { "Content-Type": "application/problem+json" } })
          : handler(request);
      },
    });

    expect(await screen.findByText("Needs the admin role")).toBeInTheDocument();
  });
});
