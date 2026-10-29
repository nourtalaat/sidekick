import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { keys, type Investigation, type Step, type Watcher } from "../../api/investigations";
import { FLUSH_MS } from "../../api/useInvestigationStream";
import { detail, INVESTIGATION_ID, RESPONDER, step, watcher } from "../../test/fixtures";
import { json, problem, renderApp, VIEWER } from "../../test/helpers";
import { FakeEventSource, routes } from "../../test/streams";
import type { Me } from "../../session";

const PATH = `/api/v1/investigations/${INVESTIGATION_ID}`;
const ROUTE = `/investigations/${INVESTIGATION_ID}`;

interface Data {
  inv?: Investigation;
  steps?: Step[];
  watchers?: Watcher[];
}

/** A fake server for one investigation, which also records the requests that change something. */
function pageServer(data: Data = {}, extra: Parameters<typeof routes>[0] = {}) {
  const posted: { path: string; csrf: string | null; body: string }[] = [];

  const post = (result: unknown, status = 200) => async (request: Request) => {
    posted.push({ path: new URL(request.url).pathname, csrf: request.headers.get("X-CSRF-Token"), body: await request.text() });

    return json(status, result);
  };

  const handler = routes({
    [`GET ${PATH}`]: () => json(200, data.inv ?? detail()),
    [`GET ${PATH}/steps`]: () => json(200, { steps: data.steps ?? [] }),
    [`GET ${PATH}/watchers`]: () => json(200, { watchers: data.watchers ?? [] }),
    [`GET ${PATH}/feedback`]: () => json(200, { parts: [] }),
    [`POST ${PATH}/follow-ups`]: post({ added: true }, 202),
    [`POST ${PATH}/cancel`]: post({ requested: true }),
    [`POST ${PATH}/resolve`]: post({ resolved: true }),
    [`POST /api/v1/watchers/${watcher().id}/stop`]: post({ stopped: true }),
    ...extra,
  });

  return { handler, posted };
}

beforeEach(() => {
  FakeEventSource.reset();
  vi.stubGlobal("EventSource", FakeEventSource);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("the investigation page", () => {
  it("shows the verdict first, then the report, the side column and the record", async () => {
    const { handler } = pageServer({ steps: [step(1, { parts: [{ type: "tool_call", id: "c1", name: "search_logs", input: { query: "errors" } }] })] });
    renderApp({ route: ROUTE, handler });

    expect(await screen.findByRole("heading", { name: "Checkout p99 latency above 2s", level: 1 })).toBeInTheDocument();

    const verdict = screen.getByRole("region", { name: "Verdict" });
    expect(within(verdict).getByText("Real issue")).toBeInTheDocument();
    expect(within(verdict).getByText("High confidence")).toBeInTheDocument();
    expect(within(verdict).getByText("A deploy caused it.")).toBeInTheDocument();

    expect(screen.getByRole("heading", { name: "Likely cause" })).toBeInTheDocument();
    expect(screen.getByText("Deploy 4f2c1a")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Findings" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Proposed fixes" })).toBeInTheDocument();
    expect(screen.getByText("Hypothesis")).toBeInTheDocument();

    const side = screen.getByRole("complementary", { name: "About this investigation" });
    expect(within(side).getByText("signoz")).toBeInTheDocument();
    expect(within(side).getAllByText("Checked")).toHaveLength(2);
    expect(within(side).getByText("Unavailable")).toBeInTheDocument();
    expect(within(side).getByText("big-model")).toBeInTheDocument();
    expect(within(side).getByText("$0.0120")).toBeInTheDocument();

    expect(await screen.findByRole("button", { name: /Model turn: called search_logs/ })).toBeInTheDocument();
  });

  it("opens a step to show the call in a box with a copy button", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });

    const { handler } = pageServer({
      steps: [
        step(1, { parts: [{ type: "tool_call", id: "c1", name: "search_logs", input: { query: "errors" } }] }),
        step(2, { kind: "tool_result", tool: "search_logs", source: "signoz", parts: [{ type: "tool_result", callId: "c1", content: "3 errors found" }] }),
      ],
    });
    renderApp({ route: ROUTE, handler });

    const toggle = await screen.findByRole("button", { name: /Model turn: called search_logs/ });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText(/"query": "errors"/)).not.toBeInTheDocument();

    await user.click(toggle);

    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText(/"query": "errors"/)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Copy" }));

    expect(writeText).toHaveBeenCalledWith(JSON.stringify({ query: "errors" }, null, 2));
    expect(await screen.findByText("Copied to the clipboard")).toBeInTheDocument();
  });

  it("shows text from alerts and from the model as plain text, and links only to web addresses", async () => {
    const hostile = detail({
      title: "<img src=x onerror=alert(1)> alert",
      report: {
        ...detail().report!,
        reason: "<script>alert('reason')</script>",
        cause: "<b>bold</b> cause",
      },
    });
    const { handler } = pageServer({ inv: hostile });
    const { container } = renderApp({ route: ROUTE, handler });

    expect(await screen.findByRole("heading", { level: 1 })).toHaveTextContent("<img src=x onerror=alert(1)> alert");
    expect(screen.getByText("<script>alert('reason')</script>")).toBeInTheDocument();
    expect(screen.getByText("<b>bold</b> cause")).toBeInTheDocument();
    expect(container.querySelector("script, img[onerror], b")).toBeNull();

    expect(screen.getByRole("link", { name: "SigNoz dashboard" })).toHaveAttribute("href", "https://signoz.example.com/d/1");
    expect(screen.queryByRole("link", { name: "Local file" })).not.toBeInTheDocument();
    expect(screen.getByText("Local file")).toBeInTheDocument();

    await userEvent.setup().click(screen.getByText(/alerts in this group/));
    expect(screen.getByRole("link", { name: "Runbook" })).toHaveAttribute("href", "https://runbooks.example.com/checkout");
    expect(screen.queryByRole("link", { name: "Bad link" })).not.toBeInTheDocument();
    expect(container.querySelector('a[href^="javascript"]')).toBeNull();
  });

  it("shows the watcher state in a status region with the name of who stopped it", async () => {
    const { handler } = pageServer({
      watchers: [
        watcher({ id: "w1", name: "health", status: "stopped", stopReason: "responder", endedAt: "2026-10-26T14:26:00Z", stoppedBy: { name: "Grace Hopper" } }),
        watcher({ id: "w2", name: "disk", status: "recovered", endedAt: "2026-10-26T14:30:00Z" }),
      ],
    });
    renderApp({ route: ROUTE, handler });

    const region = await screen.findByRole("status", { name: "Watchers" });

    expect(within(region).getByText("Stopped: health")).toBeInTheDocument();
    expect(within(region).getByText(/Stopped by Grace Hopper/)).toBeInTheDocument();
    expect(within(region).getByText("Back up: disk")).toBeInTheDocument();
    expect(within(region).queryByRole("button")).not.toBeInTheDocument();
  });

  describe("buttons by role", () => {
    const running = detail({ status: "running", verdict: undefined, confidence: undefined, report: undefined, finishedAt: undefined });
    const data: Data = { inv: running, watchers: [watcher()] };

    const BUTTONS = ["Ask a follow-up", "Stop", "Resolve", "Stop watching"];

    it.each<{ name: string; me: Me; shown: boolean }>([
      { name: "a viewer", me: VIEWER, shown: false },
      { name: "a responder", me: RESPONDER, shown: true },
      { name: "an admin", me: { ...RESPONDER, role: "admin" }, shown: true },
    ])("$name sees the action buttons: $shown", async ({ me, shown }) => {
      renderApp({ me, route: ROUTE, handler: pageServer(data).handler });

      await screen.findByRole("heading", { level: 1 });
      await screen.findByText(/Checking: health/);

      for (const name of BUTTONS) {
        const found = screen.queryByRole("button", { name });
        if (shown) {
          expect(found, name).toBeInTheDocument();
        } else {
          expect(found, name).not.toBeInTheDocument();
        }
      }

      if (shown) {
        expect(screen.getByRole("form", { name: "Ask a follow-up" })).toBeInTheDocument();
      } else {
        expect(screen.queryByRole("form", { name: "Ask a follow-up" })).not.toBeInTheDocument();
      }

      expect(screen.getByRole("button", { name: "Copy link" })).toBeInTheDocument();
    });

    it("shows no Stop for a run that has ended, and no Resolve for a group that is closed", async () => {
      const ended = detail({ groupStatus: "closed", groupClosedReason: "window_ended" });
      renderApp({ me: RESPONDER, route: ROUTE, handler: pageServer({ inv: ended }).handler });

      await screen.findByRole("heading", { level: 1 });

      expect(screen.queryByRole("button", { name: "Stop" })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Resolve" })).not.toBeInTheDocument();
    });
  });

  it("sends Stop, Resolve and Stop watching with the CSRF token", async () => {
    const user = userEvent.setup();
    const running = detail({ status: "running", verdict: undefined, confidence: undefined, report: undefined });
    const { handler, posted } = pageServer({ inv: running, watchers: [watcher()] });
    renderApp({ me: RESPONDER, route: ROUTE, handler });

    await user.click(await screen.findByRole("button", { name: "Stop" }));
    await user.click(screen.getByRole("button", { name: "Resolve" }));
    await user.click(screen.getByRole("button", { name: "Stop watching" }));

    await waitFor(() => expect(posted).toHaveLength(3));
    expect(posted.map((p) => p.path)).toEqual([`${PATH}/cancel`, `${PATH}/resolve`, `/api/v1/watchers/${watcher().id}/stop`]);
    expect(posted.every((p) => p.csrf === "csrf-grace")).toBe(true);
  });

  it("shows why an action was refused", async () => {
    const user = userEvent.setup();
    const { handler } = pageServer({}, { [`POST ${PATH}/resolve`]: () => problem(409, "The group closed when its window ended.") });
    renderApp({ me: RESPONDER, route: ROUTE, handler });

    await user.click(await screen.findByRole("button", { name: "Resolve" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("The group closed when its window ended.");
  });

  describe("the follow-up box", () => {
    it("sends the question, clears the box and says it was sent", async () => {
      const user = userEvent.setup();
      const { handler, posted } = pageServer();
      renderApp({ me: RESPONDER, route: ROUTE, handler });

      const send = await screen.findByRole("button", { name: "Send question" });
      expect(send).toBeDisabled();

      await user.type(screen.getByRole("textbox", { name: "Ask a follow-up" }), "  Was it the cache?  ");
      expect(send).toBeEnabled();
      await user.click(send);

      expect(await screen.findByText("Question sent. Sidekick answers in a new run.")).toBeInTheDocument();
      expect(JSON.parse(posted[0]!.body)).toEqual({ text: "Was it the cache?" });
      expect(posted[0]!.csrf).toBe("csrf-grace");
      expect(screen.getByRole("textbox", { name: "Ask a follow-up" })).toHaveValue("");
    });

    it("shows the refusal for a triage-only result in the API's words", async () => {
      const user = userEvent.setup();
      const refusal = "This was a triage-only result, so there is no investigation to ask about yet.";
      const { handler } = pageServer({}, { [`POST ${PATH}/follow-ups`]: () => problem(409, refusal) });
      renderApp({ me: RESPONDER, route: ROUTE, handler });

      await user.type(await screen.findByRole("textbox", { name: "Ask a follow-up" }), "Why?");
      await user.click(screen.getByRole("button", { name: "Send question" }));

      expect(await screen.findByRole("alert")).toHaveTextContent(refusal);
      expect(screen.getByRole("textbox", { name: "Ask a follow-up" })).toHaveValue("Why?");
    });

    it("does not send a question over the limit", async () => {
      const user = userEvent.setup();
      renderApp({ me: RESPONDER, route: ROUTE, handler: pageServer().handler });

      const box = await screen.findByRole("textbox", { name: "Ask a follow-up" });
      await user.click(box);
      await user.paste("a".repeat(4001));

      expect(screen.getByText(/The question is too long/)).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Send question" })).toBeDisabled();
    });

    it("moves focus to the box from the header button", async () => {
      const user = userEvent.setup();
      renderApp({ me: RESPONDER, route: ROUTE, handler: pageServer().handler });

      await user.click(await screen.findByRole("button", { name: "Ask a follow-up" }));

      expect(screen.getByRole("textbox", { name: "Ask a follow-up" })).toHaveFocus();
    });
  });

  describe("the live stream", () => {
    const running = detail({ status: "running", verdict: undefined, confidence: undefined, report: undefined, finishedAt: undefined, pendingFollowUps: [] });
    const STREAM_WAIT = FLUSH_MS + 3000;

    it("follows a run from the last saved step and adds the steps that arrive", async () => {
      const { handler } = pageServer({ inv: running, steps: [step(1), step(2)] });
      renderApp({ route: ROUTE, handler });

      await screen.findByRole("button", { name: /Model turn: Thinking about step 2/ });
      await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
      expect(FakeEventSource.last().url).toMatch(/\/investigations\/[^/]+\/stream\?after=2$/);

      FakeEventSource.last().emit("step", step(3, { parts: [{ type: "text", text: "A new step" }] }), "3");

      expect(await screen.findByRole("button", { name: /Model turn: A new step/ }, { timeout: STREAM_WAIT })).toBeInTheDocument();
      expect(screen.getByRole("list", { name: "Step by step" }).getAttribute("aria-live")).toBe("polite");
    }, 15_000);

    it("does not open the stream for an investigation that has ended", async () => {
      renderApp({ route: ROUTE, handler: pageServer({ steps: [step(1)] }).handler });

      await screen.findByRole("button", { name: /Model turn/ });

      expect(FakeEventSource.instances).toHaveLength(0);
      expect(screen.getByRole("list", { name: "Step by step" }).getAttribute("aria-live")).toBe("off");
    });

    it("opens the stream when a follow-up question is posted from the page, once the run it waits for exists", async () => {
      const user = userEvent.setup();
      let current = detail();
      const { handler } = pageServer(
        { steps: [step(1), step(2)] },
        {
          [`GET ${PATH}`]: () => json(200, current),
          [`POST ${PATH}/follow-ups`]: async (request) => {
            const { text } = (await request.json()) as { text: string };
            current = { ...current, pendingFollowUps: [{ text, author: "Grace Hopper", createdAt: "2026-10-26T15:00:00Z" }] };

            return json(202, { added: true });
          },
        },
      );
      renderApp({ me: RESPONDER, route: ROUTE, handler });

      await user.type(await screen.findByRole("textbox", { name: "Ask a follow-up" }), "Was it the cache?");
      expect(FakeEventSource.instances).toHaveLength(0);

      await user.click(screen.getByRole("button", { name: "Send question" }));

      expect(await screen.findByRole("heading", { name: "Waiting for an answer" })).toBeInTheDocument();
      await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
      expect(FakeEventSource.last().url).toMatch(/stream\?after=2$/);
    });

    it("opens the stream when a read of the investigation shows a new run, such as a follow-up asked in Slack", async () => {
      let current = detail();
      const queryClient = new QueryClient();
      const { handler } = pageServer({ steps: [step(1)] }, { [`GET ${PATH}`]: () => json(200, current) });
      renderApp({ route: ROUTE, handler, queryClient });

      await screen.findByRole("button", { name: /Model turn/ });
      expect(FakeEventSource.instances).toHaveLength(0);

      current = { ...current, status: "running", run: 2, runKind: "follow_up" };
      await queryClient.invalidateQueries({ queryKey: keys.investigation(INVESTIGATION_ID) });

      await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
      expect(await screen.findByText("Investigating")).toBeInTheDocument();
    });

    it("closes the stream when the status shows the run has ended", async () => {
      let current = running;
      const queryClient = new QueryClient();
      const { handler } = pageServer({ steps: [step(1)] }, { [`GET ${PATH}`]: () => json(200, current) });
      renderApp({ route: ROUTE, handler, queryClient });

      await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
      const source = FakeEventSource.last();

      current = detail();
      source.emit("status", { status: "final" });

      await waitFor(() => expect(source.closed).toBe(true));
      expect(await screen.findByText("A deploy caused it.")).toBeInTheDocument();
    });

    it("reads the watchers again when the stream says they changed", async () => {
      let state: Watcher[] = [watcher()];
      const { handler } = pageServer({ inv: running }, { [`GET ${PATH}/watchers`]: () => json(200, { watchers: state }) });
      renderApp({ route: ROUTE, handler });

      await screen.findByText(/Checking: health/);
      await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));

      state = [watcher({ status: "recovered", endedAt: "2026-10-26T14:30:00Z" })];
      FakeEventSource.last().emit("watchers", {});

      expect(await screen.findByText(/Back up: health/)).toBeInTheDocument();
    });
  });

  it("shows the failure when the investigation does not exist", async () => {
    renderApp({ route: ROUTE, handler: pageServer({}, { [`GET ${PATH}`]: () => problem(404, "No investigation has this ID.") }).handler });

    expect(await screen.findByRole("alert")).toHaveTextContent("No investigation has this ID.");
  });
});
