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

  it("says that the route's policy approved a proposal, with no person named", () => {
    renderWithProviders(<ProposalCard proposal={proposal({ status: "executing", mode: "read-write", approvedByPolicy: true, decidedAt: "2026-10-26T14:02:00Z" })} canDecide />);

    expect(screen.getByText("Decided by")).toBeInTheDocument();
    expect(screen.getByText("Approved by policy (read + write route)")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Approve/ })).not.toBeInTheDocument();
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

    expect(sent).toHaveLength(0);
    expect(screen.getByText("Run monitor-note with the parameters above?")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Approve it" }));

    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]).toEqual({ path: `${BASE}/approve`, csrf: "csrf-ada", body: undefined });
  });

  it("asks before it approves, and sends nothing if the person is not sure", async () => {
    const user = userEvent.setup();
    const { api, sent } = server();
    renderWithProviders(<ProposalCard proposal={proposal()} canDecide />, { api });

    await user.click(screen.getByRole("button", { name: "Approve monitor-note" }));

    expect(screen.getByRole("button", { name: "Approve it" })).toHaveFocus();

    await user.click(screen.getByRole("button", { name: "Not yet" }));

    expect(screen.queryByText("Run monitor-note with the parameters above?")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve monitor-note" })).toBeInTheDocument();
    expect(sent).toHaveLength(0);
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
    await user.click(screen.getByRole("button", { name: "Approve it" }));

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

  it("has an anchor that can take focus from a link, and is no tab stop", () => {
    renderWithProviders(<ProposalCard proposal={proposal()} canDecide={false} />);

    const card = screen.getByRole("article", { name: "Proposed action monitor-note" });
    expect(card).toHaveAttribute("id", `proposal-${PROPOSAL_ID}`);
    expect(card).toHaveAttribute("tabindex", "-1");
  });

  it("names and links the investigation in the review queue", () => {
    renderWithProviders(<ProposalCard proposal={proposal()} canDecide inQueue />);

    expect(screen.getByRole("link", { name: "checkout-api p99 latency above 2s" })).toHaveAttribute("href", "/investigations/0192d3a4-0000-7000-8000-0000000000e2");
  });

  it("has no WCAG 2.2 AA violations, also while it asks to confirm", async () => {
    const user = userEvent.setup();
    const { container } = renderWithProviders(<ProposalCard proposal={proposal({ status: "proposed" })} canDecide />);

    expect(await wcagViolations(container)).toEqual([]);

    await user.click(screen.getByRole("button", { name: "Approve monitor-note" }));

    expect(await wcagViolations(container)).toEqual([]);
  });

  describe("a change to a definition", () => {
    const CHANGES = [
      { path: "options.thresholds.critical", before: "2", after: "3" },
      { path: "tags", before: '["a"]' },
      { path: "renotify", after: "30" },
    ];

    it("shows each field with its values, and says when a side has none", () => {
      renderWithProviders(<ProposalCard proposal={proposal({ action: "datadog-monitor", target: "datadog:monitor/123", changes: CHANGES })} canDecide />);

      const section = screen.getByRole("region", { name: "Changes, field by field" });
      expect(section).toHaveTextContent("options.thresholds.critical");
      expect(section).toHaveTextContent("Before2After3");
      expect(section).toHaveTextContent('Before["a"]After(removed)');
      expect(section).toHaveTextContent("Before(not set)After30");
    });

    it("gives each field a Before and an After side that a screen reader names with the field", () => {
      renderWithProviders(<ProposalCard proposal={proposal({ changes: CHANGES })} canDecide />);

      for (const change of CHANGES) {
        expect(screen.getByRole("heading", { name: change.path, level: 5 })).toBeInTheDocument();
        expect(screen.getByRole("region", { name: `Before, ${change.path}` })).toBeInTheDocument();
        expect(screen.getByRole("region", { name: `After, ${change.path}` })).toBeInTheDocument();
      }
    });

    it("names the whole definition, and scrolls and focuses a long value", async () => {
      const user = userEvent.setup();
      const long = '{"query":"' + "x".repeat(5000) + '"}';
      renderWithProviders(<ProposalCard proposal={proposal({ changes: [{ path: "", before: long }] })} canDecide />);

      const before = screen.getByRole("region", { name: "Before, (the whole definition)" });
      expect(before).toHaveTextContent(long);
      expect(before).toHaveAttribute("tabindex", "0");
      expect(screen.getByRole("region", { name: "After, (the whole definition)" })).toHaveTextContent("(removed)");

      await user.tab();
      await user.tab();

      expect(before).toHaveFocus();
    });

    it("shows the values as text, never as markup", () => {
      const { container } = renderWithProviders(
        <ProposalCard proposal={proposal({ changes: [{ path: "message", before: '"<img src=x onerror=alert(1)>"', after: '"<b>bold</b>"' }] })} canDecide />,
      );

      expect(container.querySelector("img")).toBeNull();
      expect(container.querySelector("b")).toBeNull();
      expect(screen.getByText('"<b>bold</b>"')).toBeInTheDocument();
    });

    it("asks about the changes when it asks to approve", async () => {
      const user = userEvent.setup();
      renderWithProviders(<ProposalCard proposal={proposal({ changes: CHANGES })} canDecide />);

      await user.click(screen.getByRole("button", { name: "Approve monitor-note" }));

      expect(screen.getByText("Run monitor-note with the parameters and the changes above?")).toBeInTheDocument();
    });

    it("says plainly that a delete cannot be undone, also when it asks to approve", async () => {
      const user = userEvent.setup();
      renderWithProviders(
        <ProposalCard proposal={proposal({ target: "datadog:monitor/123", changes: [{ path: "", before: '{"name":"x"}' }] })} canDecide />,
      );

      expect(screen.getByRole("note")).toHaveTextContent("This deletes datadog:monitor/123. Sidekick cannot undo a delete.");

      await user.click(screen.getByRole("button", { name: "Approve monitor-note" }));

      expect(screen.getByText("Delete datadog:monitor/123? Sidekick cannot undo a delete.")).toBeInTheDocument();
    });
  });

  describe("Undo", () => {
    const UNDO = `${BASE}/undo`;
    const CHANGES = [{ path: "options.thresholds.critical", before: "2", after: "3" }];
    const undoable = (over = {}) => proposal({ status: "succeeded", undoable: true, target: "datadog:monitor/123", changes: CHANGES, ...over });

    /** A fake server that records the undo requests and answers each from answers, in order. */
    function undoServer(...answers: (() => Response)[]) {
      const sent: { csrf: string | null; body: unknown }[] = [];

      const { api } = fakeServer(
        APPROVER,
        routes({
          [`POST ${UNDO}`]: async (request: Request) => {
            sent.push({ csrf: request.headers.get("X-CSRF-Token"), body: JSON.parse(await request.clone().text()) as unknown });
            const answer = answers[Math.min(sent.length, answers.length) - 1];

            return answer ? answer() : json(200, proposal({ status: "succeeded", undoOf: PROPOSAL_ID }));
          },
        }),
      );

      return { api, sent };
    }

    it("asks once, then undoes without force", async () => {
      const user = userEvent.setup();
      const { api, sent } = undoServer();
      api.setCsrfToken("csrf-ada");
      renderWithProviders(<ProposalCard proposal={undoable()} canDecide />, { api });

      await user.click(screen.getByRole("button", { name: "Undo monitor-note" }));

      expect(sent).toHaveLength(0);
      expect(screen.getByText(/Put back the fields this change set in datadog:monitor\/123\?/)).toBeInTheDocument();

      await user.click(screen.getByRole("button", { name: "Undo it" }));

      await waitFor(() => expect(sent).toHaveLength(1));
      expect(sent[0]).toEqual({ csrf: "csrf-ada", body: { force: false } });
    });

    it("sends nothing if the person is not sure", async () => {
      const user = userEvent.setup();
      const { api, sent } = undoServer();
      renderWithProviders(<ProposalCard proposal={undoable()} canDecide />, { api });

      await user.click(screen.getByRole("button", { name: "Undo monitor-note" }));
      await user.click(screen.getByRole("button", { name: "Not now" }));

      expect(screen.getByRole("button", { name: "Undo monitor-note" })).toBeInTheDocument();
      expect(sent).toHaveLength(0);
    });

    it("shows what a restore would overwrite and restores only when asked again", async () => {
      const user = userEvent.setup();
      const conflict = () =>
        json(409, {
          type: "about:blank",
          title: "Conflict",
          status: 409,
          code: "conflict",
          detail: "The definition changed after Sidekick changed it. Restoring it overwrites the fields below.",
          changes: [{ path: "options.thresholds.critical", before: "95", after: "2" }],
        });
      const { api, sent } = undoServer(conflict);
      renderWithProviders(<ProposalCard proposal={undoable()} canDecide />, { api });

      await user.click(screen.getByRole("button", { name: "Undo monitor-note" }));
      await user.click(screen.getByRole("button", { name: "Undo it" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("The definition changed after Sidekick changed it.");

      const overwrite = screen.getByRole("region", { name: "What a restore would overwrite" });
      expect(overwrite).toHaveTextContent("options.thresholds.critical");
      expect(overwrite).toHaveTextContent("Now95Restored to2");
      expect(sent).toHaveLength(1);

      await user.click(screen.getByRole("button", { name: "Restore anyway" }));

      await waitFor(() => expect(sent).toHaveLength(2));
      expect(sent[1]?.body).toEqual({ force: true });
    });

    it("lets the person leave it after a conflict", async () => {
      const user = userEvent.setup();
      const { api, sent } = undoServer(() => json(409, { type: "about:blank", title: "Conflict", status: 409, code: "conflict", detail: "Changed.", changes: [] }));
      renderWithProviders(<ProposalCard proposal={undoable()} canDecide />, { api });

      await user.click(screen.getByRole("button", { name: "Undo monitor-note" }));
      await user.click(screen.getByRole("button", { name: "Undo it" }));
      await user.click(await screen.findByRole("button", { name: "Leave it" }));

      expect(screen.getByRole("button", { name: "Undo monitor-note" })).toBeInTheDocument();
      expect(sent).toHaveLength(1);
    });

    it("shows why an undo was refused, as the server says it", async () => {
      const user = userEvent.setup();
      const { api } = undoServer(() => problem(409, "This proposal cannot be undone: it was undone already."));
      renderWithProviders(<ProposalCard proposal={undoable()} canDecide />, { api });

      await user.click(screen.getByRole("button", { name: "Undo monitor-note" }));
      await user.click(screen.getByRole("button", { name: "Undo it" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("This proposal cannot be undone: it was undone already.");
      expect(screen.queryByRole("button", { name: "Restore anyway" })).not.toBeInTheDocument();
    });

    it("has no Undo for someone who cannot decide, or for a change that cannot be undone", () => {
      const { unmount } = renderWithProviders(<ProposalCard proposal={undoable()} canDecide={false} />);
      expect(screen.queryByRole("button", { name: /Undo/ })).not.toBeInTheDocument();
      unmount();

      renderWithProviders(<ProposalCard proposal={undoable({ undoable: false })} canDecide />);
      expect(screen.queryByRole("button", { name: /Undo/ })).not.toBeInTheDocument();
    });

    it("shows an undo as the proposal it restores, with no reason", () => {
      renderWithProviders(<ProposalCard proposal={proposal({ status: "succeeded", undoOf: "0192d3a4-0000-7000-8000-0000000000f9", reason: "" })} canDecide />);

      expect(screen.getByRole("link", { name: "monitor-note proposal" })).toHaveAttribute(
        "href",
        "/investigations/0192d3a4-0000-7000-8000-0000000000e2#proposal-0192d3a4-0000-7000-8000-0000000000f9",
      );
      expect(screen.queryByText(/0192d3a4-0000-7000-8000-0000000000f9/)).not.toBeInTheDocument();
      expect(screen.queryByText("Reason, written by the model and not checked")).not.toBeInTheDocument();
    });

    it("has no WCAG 2.2 AA violations, also with the difference shown", async () => {
      const user = userEvent.setup();
      const { api } = undoServer(() => json(409, { type: "about:blank", title: "Conflict", status: 409, code: "conflict", detail: "Changed.", changes: CHANGES }));
      const { container } = renderWithProviders(<ProposalCard proposal={undoable()} canDecide />, { api });

      expect(await wcagViolations(container)).toEqual([]);

      await user.click(screen.getByRole("button", { name: "Undo monitor-note" }));
      await user.click(screen.getByRole("button", { name: "Undo it" }));
      await screen.findByRole("button", { name: "Restore anyway" });

      expect(await wcagViolations(container)).toEqual([]);
    });
  });
});
