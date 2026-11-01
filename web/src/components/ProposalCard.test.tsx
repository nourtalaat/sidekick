import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { json, problem, renderWithProviders, fakeServer, wcagViolations } from "../test/helpers";
import { APPROVER, PROPOSAL_ID, proposal } from "../test/actionProposals";
import { routes } from "../test/streams";
import { ProposalCard } from "./ProposalCard";

const BASE = `/api/v1/proposals/${PROPOSAL_ID}`;

/** A fake server that records the decisions sent to it and answers with answer. */
function server(answer: () => Response = () => json(200, proposal({ status: "approved", decidedByName: "Ada Lovelace" }))) {
  const sent: { path: string; csrf: string | null; body: unknown }[] = [];

  const record = async (request: Request) => {
    const text = await request.clone().text();
    sent.push({ path: new URL(request.url).pathname, csrf: request.headers.get("X-CSRF-Token"), body: text === "" ? undefined : (JSON.parse(text) as unknown) });

    return answer();
  };

  const { api } = fakeServer(APPROVER, routes({ [`POST ${BASE}/approve`]: record, [`POST ${BASE}/reject`]: record }));

  return { api, sent };
}

describe("ProposalCard", () => {
  it("shows the parameters whole and the reason as unchecked text", () => {
    renderWithProviders(<ProposalCard proposal={proposal()} canDecide />);

    expect(screen.getByText("Parameters, exactly as they will run")).toBeInTheDocument();
    expect(screen.getByText(/"title": "Slow checkout"/)).toBeInTheDocument();
    expect(screen.getByText("Reason, written by the model and not checked")).toBeInTheDocument();
    expect(screen.getByText("p99 doubled after the deploy.")).toBeInTheDocument();
    expect(screen.getByText("Waiting for a decision")).toBeInTheDocument();
  });

  it("shows text from the model as text, never as markup", () => {
    const { container } = renderWithProviders(
      <ProposalCard proposal={proposal({ reason: "<img src=x onerror=alert(1)> <b>bold</b>", paramsText: '{\n  "t": "<script>alert(1)</script>"\n}' })} canDecide />,
    );

    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("script")).toBeNull();
    expect(container.querySelector("b")).toBeNull();
    expect(screen.getByText("<img src=x onerror=alert(1)> <b>bold</b>")).toBeInTheDocument();
    expect(screen.getByText(/<script>alert\(1\)<\/script>/)).toBeInTheDocument();
  });

  it("approves with the session's CSRF token", async () => {
    const user = userEvent.setup();
    const { api, sent } = server();
    api.setCsrfToken("csrf-ada");
    renderWithProviders(<ProposalCard proposal={proposal()} canDecide />, { api });

    await user.click(screen.getByRole("button", { name: "Approve monitor-note" }));

    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]).toEqual({ path: `${BASE}/approve`, csrf: "csrf-ada", body: undefined });
  });

  it("rejects with a note", async () => {
    const user = userEvent.setup();
    const { api, sent } = server(() => json(200, proposal({ status: "rejected" })));
    renderWithProviders(<ProposalCard proposal={proposal()} canDecide />, { api });

    await user.click(screen.getByRole("button", { name: "Reject monitor-note" }));
    await user.type(screen.getByLabelText("Why reject it? (optional)"), "wrong board");
    await user.click(screen.getByRole("button", { name: "Reject it" }));

    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]?.path).toBe(`${BASE}/reject`);
    expect(sent[0]?.body).toEqual({ note: "wrong board" });
  });

  it("lets the person change their mind before rejecting", async () => {
    const user = userEvent.setup();
    const { api, sent } = server();
    renderWithProviders(<ProposalCard proposal={proposal()} canDecide />, { api });

    await user.click(screen.getByRole("button", { name: "Reject monitor-note" }));
    await user.click(screen.getByRole("button", { name: "Keep it" }));

    expect(screen.queryByLabelText("Why reject it? (optional)")).not.toBeInTheDocument();
    expect(sent).toHaveLength(0);
  });

  it("shows why a decision was refused, as the server says it", async () => {
    const user = userEvent.setup();
    const { api } = server(() => problem(409, "This proposal is rejected already; Bo decided it."));
    renderWithProviders(<ProposalCard proposal={proposal()} canDecide />, { api });

    await user.click(screen.getByRole("button", { name: "Approve monitor-note" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("This proposal is rejected already; Bo decided it.");
  });

  it("gives someone who cannot decide the proposal and no buttons", () => {
    renderWithProviders(<ProposalCard proposal={proposal()} canDecide={false} />);

    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.getByText("Only approvers can approve or reject it.")).toBeInTheDocument();
  });

  it.each(["approved", "rejected", "expired", "executing", "succeeded", "failed"] as const)("has no buttons once it is %s", (status) => {
    renderWithProviders(<ProposalCard proposal={proposal({ status })} canDecide />);

    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("shows who decided and what the action made", () => {
    renderWithProviders(
      <ProposalCard
        proposal={proposal({
          status: "succeeded",
          decidedByName: "Ana",
          result: { summary: "Wrote a note.", links: [{ title: "Note", url: "https://notes.example.com/n/1" }, { title: "Bad", url: "javascript:alert(1)" }] },
        })}
        canDecide
      />,
    );

    expect(screen.getByText("Ana")).toBeInTheDocument();
    expect(screen.getByText("Wrote a note.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Note" })).toHaveAttribute("href", "https://notes.example.com/n/1");
    expect(screen.queryByRole("link", { name: "Bad" })).not.toBeInTheDocument();
    expect(screen.getByText("Bad")).toBeInTheDocument();
  });

  it("says why it failed", () => {
    renderWithProviders(<ProposalCard proposal={proposal({ status: "failed", error: "The notes service is down." })} canDecide />);

    expect(screen.getByText("Why it failed")).toBeInTheDocument();
    expect(screen.getByText("The notes service is down.")).toBeInTheDocument();
  });

  it("names and links the investigation in the review queue", () => {
    renderWithProviders(<ProposalCard proposal={proposal()} canDecide inQueue />);

    expect(screen.getByRole("link", { name: "checkout-api p99 latency above 2s" })).toHaveAttribute("href", "/investigations/0192d3a4-0000-7000-8000-0000000000e2");
  });

  it("has no WCAG 2.2 AA violations", async () => {
    const { container } = renderWithProviders(<ProposalCard proposal={proposal({ status: "proposed" })} canDecide />);

    expect(await wcagViolations(container)).toEqual([]);
  });
});
