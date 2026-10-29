import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { FeedbackPart } from "../../api/feedback";
import type { Investigation, Step } from "../../api/investigations";
import { detail, INVESTIGATION_ID, RESPONDER, step } from "../../test/fixtures";
import { json, problem, renderApp, VIEWER } from "../../test/helpers";
import { FakeEventSource, routes } from "../../test/streams";

const PATH = `/api/v1/investigations/${INVESTIGATION_ID}`;
const ROUTE = `/investigations/${INVESTIGATION_ID}`;

const VERDICT_REF = "run:1/verdict";
const FINDING_REF = "run:1/finding:f1";

interface Sent {
  csrf: string | null;
  body: Record<string, unknown>;
}

interface Data {
  inv?: Investigation;
  steps?: Step[];
  parts?: FeedbackPart[];
  /** The answer to POST /feedback; a 201 echo by default. */
  answer?: () => Response;
  /** Makes GET /feedback fail. */
  feedbackFails?: boolean;
}

/** A fake server for one investigation that records what is sent to POST /feedback and counts the reads of the counts. */
function feedbackServer(data: Data = {}) {
  const sent: Sent[] = [];
  const reads = { feedback: 0 };

  const handler = routes({
    [`GET ${PATH}`]: () => json(200, data.inv ?? detail()),
    [`GET ${PATH}/steps`]: () => json(200, { steps: data.steps ?? [] }),
    [`GET ${PATH}/watchers`]: () => json(200, { watchers: [] }),
    [`GET ${PATH}/feedback`]: () => {
      reads.feedback += 1;

      return data.feedbackFails ? problem(500, "Something went wrong.") : json(200, { parts: data.parts ?? [] });
    },
    [`POST ${PATH}/feedback`]: async (request) => {
      sent.push({ csrf: request.headers.get("X-CSRF-Token"), body: (await request.json()) as Record<string, unknown> });

      return data.answer ? data.answer() : json(201, { id: "f", confirmation: "unchanged" });
    },
  });

  return { handler, sent, reads };
}

const part = (targetKind: FeedbackPart["targetKind"], targetRef: string, correct: number, wrong: number, mine?: FeedbackPart["mine"]): FeedbackPart => ({
  targetKind,
  targetRef,
  correct,
  wrong,
  mine,
});

beforeEach(() => {
  FakeEventSource.reset();
  vi.stubGlobal("EventSource", FakeEventSource);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("feedback on the parts of an investigation", () => {
  it("shows a viewer how many people rated each part, and no buttons", async () => {
    const { handler } = feedbackServer({
      parts: [part("verdict", VERDICT_REF, 2, 1), part("finding", FINDING_REF, 0, 3)],
      steps: [step(2, { kind: "tool_result", tool: "search_logs", parts: [{ type: "tool_result", callId: "c1", content: "3 errors" }] })],
    });
    renderApp({ me: VIEWER, route: ROUTE, handler });

    const verdict = await screen.findByRole("region", { name: "Verdict" });
    expect(await within(verdict).findByText("2 correct, 1 wrong")).toBeInTheDocument();
    expect(await screen.findByText("0 correct, 3 wrong")).toBeInTheDocument();

    expect(screen.queryByText("No feedback yet")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Mark .* (correct|wrong)/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Add a reason/ })).not.toBeInTheDocument();
  });

  it("gives a responder buttons on the verdict, each finding, each fix and each tool call", async () => {
    const { handler } = feedbackServer({
      steps: [step(2, { kind: "tool_result", tool: "search_logs", callId: "c1", parts: [{ type: "tool_result", callId: "c1", content: "3 errors" }] })],
    });
    renderApp({ me: RESPONDER, route: ROUTE, handler });

    const verdict = await screen.findByRole("region", { name: "Verdict" });
    expect(within(verdict).getByRole("button", { name: "Mark the verdict correct" })).toBeInTheDocument();
    expect(within(verdict).getByRole("button", { name: "Mark the verdict wrong" })).toBeInTheDocument();

    for (const label of ["finding f1", "finding f2", "fix x1", "the tool call search_logs"]) {
      expect(await screen.findByRole("button", { name: `Mark ${label} correct` })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: `Mark ${label} wrong` })).toBeInTheDocument();
    }

    expect(screen.getAllByText("No feedback yet")).toHaveLength(5);
  });

  it("sends Correct at once, with the CSRF token, and says so", async () => {
    const user = userEvent.setup();
    const { handler, sent, reads } = feedbackServer();
    renderApp({ me: RESPONDER, route: ROUTE, handler });

    await user.click(await screen.findByRole("button", { name: "Mark finding f1 correct" }));

    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]).toEqual({ csrf: "csrf-grace", body: { targetKind: "finding", targetRef: FINDING_REF, rating: "correct" } });
    expect(await screen.findByText("Feedback sent")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: /^Feedback on/ })).not.toBeInTheDocument();
    await waitFor(() => expect(reads.feedback).toBeGreaterThan(1));
  });

  it("opens a form on Wrong, sends the reason, and puts the focus back on the button", async () => {
    const user = userEvent.setup();
    const { handler, sent } = feedbackServer();
    renderApp({ me: RESPONDER, route: ROUTE, handler });

    const wrong = await screen.findByRole("button", { name: "Mark finding f2 wrong" });
    await user.click(wrong);

    const form = screen.getByRole("form", { name: "Feedback on finding f2" });
    const reason = within(form).getByLabelText("Why is it wrong? (optional)");
    expect(reason).toHaveFocus();
    expect(within(form).queryByLabelText("The real verdict, if you know it")).not.toBeInTheDocument();
    expect(sent).toHaveLength(0);

    await user.type(reason, "  There was no deploy.  ");
    await user.click(within(form).getByRole("button", { name: "Send feedback" }));

    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]?.body).toEqual({ targetKind: "finding", targetRef: "run:1/finding:f2", rating: "wrong", reason: "There was no deploy." });
    await waitFor(() => expect(screen.queryByRole("form", { name: /^Feedback on/ })).not.toBeInTheDocument());
    expect(wrong).toHaveFocus();
    expect(screen.getByText("Feedback sent")).toBeInTheDocument();
  });

  it("sends a Wrong with no reason when the form is sent empty", async () => {
    const user = userEvent.setup();
    const { handler, sent } = feedbackServer();
    renderApp({ me: RESPONDER, route: ROUTE, handler });

    await user.click(await screen.findByRole("button", { name: "Mark fix x1 wrong" }));
    await user.click(screen.getByRole("button", { name: "Send feedback" }));

    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]?.body).toEqual({ targetKind: "fix", targetRef: "run:1/fix:x1", rating: "wrong" });
  });

  it("closes the form on Cancel without sending, and puts the focus back", async () => {
    const user = userEvent.setup();
    const { handler, sent } = feedbackServer();
    renderApp({ me: RESPONDER, route: ROUTE, handler });

    const wrong = await screen.findByRole("button", { name: "Mark finding f1 wrong" });
    await user.click(wrong);
    await user.type(screen.getByLabelText("Why is it wrong? (optional)"), "Hmm");
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(screen.queryByRole("form", { name: /^Feedback on/ })).not.toBeInTheDocument();
    expect(wrong).toHaveFocus();
    expect(sent).toHaveLength(0);
  });

  it("adds a reason to a Correct without the verdict fields", async () => {
    const user = userEvent.setup();
    const { handler, sent } = feedbackServer();
    renderApp({ me: RESPONDER, route: ROUTE, handler });

    const verdict = await screen.findByRole("region", { name: "Verdict" });
    await user.click(within(verdict).getByRole("button", { name: "Add a reason about the verdict" }));

    const form = screen.getByRole("form", { name: "Feedback on the verdict" });
    expect(within(form).queryByLabelText("The real verdict, if you know it")).not.toBeInTheDocument();

    await user.type(within(form).getByLabelText("Why is it correct? (optional)"), "The deploy matches.");
    await user.click(within(form).getByRole("button", { name: "Send feedback" }));

    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]?.body).toEqual({ targetKind: "verdict", targetRef: VERDICT_REF, rating: "correct", reason: "The deploy matches." });
  });

  it("asks for the real verdict and cause on a Wrong verdict, and sends them", async () => {
    const user = userEvent.setup();
    const { handler, sent } = feedbackServer();
    renderApp({ me: RESPONDER, route: ROUTE, handler });

    const verdict = await screen.findByRole("region", { name: "Verdict" });
    await user.click(within(verdict).getByRole("button", { name: "Mark the verdict wrong" }));

    const form = screen.getByRole("form", { name: "Feedback on the verdict" });
    const choice = within(form).getByLabelText("The real verdict, if you know it");
    const names = within(choice).getAllByRole("option").map((option) => option.textContent);
    expect(names).toEqual(["I do not know", "False alarm", "Not sure yet", "Duplicate", "Known issue", "Flapping"]);
    expect(within(form).queryByLabelText(/The real cause/)).not.toBeInTheDocument();

    await user.selectOptions(choice, "false_positive");
    expect(within(form).getByText("This confirms the investigation with this verdict.")).toBeInTheDocument();

    await user.type(within(form).getByLabelText("Why is it wrong? (optional)"), "It was a load test.");
    await user.type(within(form).getByLabelText("The real cause, if you know it (optional)"), "A load test.");
    await user.click(within(form).getByRole("button", { name: "Send feedback" }));

    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]?.body).toEqual({
      targetKind: "verdict",
      targetRef: VERDICT_REF,
      rating: "wrong",
      reason: "It was a load test.",
      correctVerdict: "false_positive",
      correctCause: "A load test.",
    });
  });

  it("sends no cause when no real verdict is chosen", async () => {
    const user = userEvent.setup();
    const { handler, sent } = feedbackServer();
    renderApp({ me: RESPONDER, route: ROUTE, handler });

    const verdict = await screen.findByRole("region", { name: "Verdict" });
    await user.click(within(verdict).getByRole("button", { name: "Mark the verdict wrong" }));
    await user.type(screen.getByLabelText("Why is it wrong? (optional)"), "Not right.");
    await user.click(screen.getByRole("button", { name: "Send feedback" }));

    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]?.body).toEqual({ targetKind: "verdict", targetRef: VERDICT_REF, rating: "wrong", reason: "Not right." });
  });

  it("shows the user's own rating as pressed, and starts the form with what they said", async () => {
    const user = userEvent.setup();
    const { handler } = feedbackServer({
      parts: [part("verdict", VERDICT_REF, 1, 1, { rating: "wrong", reason: "It was a load test.", correctVerdict: "false_positive", correctCause: "A load test." })],
    });
    renderApp({ me: RESPONDER, route: ROUTE, handler });

    const verdict = await screen.findByRole("region", { name: "Verdict" });
    const wrong = await within(verdict).findByRole("button", { name: "Mark the verdict wrong" });

    await waitFor(() => expect(wrong).toHaveAttribute("aria-pressed", "true"));
    expect(within(verdict).getByRole("button", { name: "Mark the verdict correct" })).toHaveAttribute("aria-pressed", "false");

    await user.click(wrong);

    const form = screen.getByRole("form", { name: "Feedback on the verdict" });
    expect(within(form).getByLabelText("Why is it wrong? (optional)")).toHaveValue("It was a load test.");
    expect(within(form).getByLabelText("The real verdict, if you know it")).toHaveValue("false_positive");
    expect(within(form).getByLabelText("The real cause, if you know it (optional)")).toHaveValue("A load test.");
  });

  it("shows a refusal of the API in its own words and keeps the form", async () => {
    const user = userEvent.setup();
    const { handler, sent } = feedbackServer({ answer: () => problem(422, "This part is not in the investigation's current report or steps.") });
    renderApp({ me: RESPONDER, route: ROUTE, handler });

    await user.click(await screen.findByRole("button", { name: "Mark finding f1 wrong" }));
    await user.type(screen.getByLabelText("Why is it wrong? (optional)"), "Not true.");
    await user.click(screen.getByRole("button", { name: "Send feedback" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("This part is not in the investigation's current report or steps.");
    expect(screen.getByLabelText("Why is it wrong? (optional)")).toHaveValue("Not true.");
    expect(sent).toHaveLength(1);
    expect(screen.queryByText("Feedback sent")).not.toBeInTheDocument();
  });

  it("shows a refusal of a Correct next to the buttons", async () => {
    const user = userEvent.setup();
    const { handler } = feedbackServer({ answer: () => problem(409, "The investigation has no verdict yet; only its tool calls can be rated.") });
    renderApp({ me: RESPONDER, route: ROUTE, handler });

    await user.click(await screen.findByRole("button", { name: "Mark fix x1 correct" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("The investigation has no verdict yet");
  });

  it("does not send a reason that is too long", async () => {
    const user = userEvent.setup();
    const { handler, sent } = feedbackServer();
    renderApp({ me: RESPONDER, route: ROUTE, handler });

    await user.click(await screen.findByRole("button", { name: "Mark finding f1 wrong" }));
    await user.click(screen.getByLabelText("Why is it wrong? (optional)"));
    await user.paste("x".repeat(2001));

    expect(screen.getByText("Keep the reason under 2000 characters.")).toBeInTheDocument();

    const send = screen.getByRole("button", { name: "Send feedback" });
    expect(send).toBeDisabled();
    await user.click(send);
    expect(sent).toHaveLength(0);
  });

  it("rates the tool call of a result by its call ID, while the investigation runs", async () => {
    const user = userEvent.setup();
    const { handler, sent } = feedbackServer({
      inv: detail({ status: "running", verdict: undefined, confidence: undefined, report: undefined }),
      steps: [step(2, { kind: "tool_result", tool: "search_logs", source: "signoz", parts: [{ type: "tool_result", callId: "c1", content: "3 errors" }], callId: "c1" })],
    });
    renderApp({ me: RESPONDER, route: ROUTE, handler });

    await user.click(await screen.findByRole("button", { name: "Mark the tool call search_logs wrong" }));
    await user.click(screen.getByRole("button", { name: "Send feedback" }));

    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]?.body).toEqual({ targetKind: "tool_call", targetRef: "c1", rating: "wrong" });
  });

  it("reads the counts again when the stream reports a change", async () => {
    const { handler, reads } = feedbackServer({ inv: detail({ status: "running" }) });
    renderApp({ me: RESPONDER, route: ROUTE, handler });

    await screen.findByRole("region", { name: "Verdict" });
    await waitFor(() => expect(reads.feedback).toBe(1));
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));

    act(() => FakeEventSource.last().emit("status", { status: "running" }));

    await waitFor(() => expect(reads.feedback).toBe(2));
  });

  it("keeps the page working when the counts cannot be read", async () => {
    const { handler } = feedbackServer({ feedbackFails: true });
    renderApp({ me: RESPONDER, route: ROUTE, handler });

    expect(await screen.findByRole("button", { name: "Mark the verdict correct" })).toBeInTheDocument();
    expect(screen.getAllByText("No feedback yet").length).toBeGreaterThan(0);
  });
});
