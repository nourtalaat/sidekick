import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { detail, INVESTIGATION_ID, RESPONDER } from "../../test/fixtures";
import { json, renderApp } from "../../test/helpers";
import { ADMIN, codedProblem, recording } from "../../test/rules";
import { scenario, scenarioDetail } from "../../test/scenarios";
import { FakeEventSource, routes } from "../../test/streams";
import type { Investigation } from "../../api/investigations";

const PATH = `/api/v1/investigations/${INVESTIGATION_ID}`;
const ROUTE = `/investigations/${INVESTIGATION_ID}`;

const CONFIRMED: Investigation = detail({ confirmation: { verdict: "true_positive", at: "2026-10-26T15:00:00Z" } });

/** A fake server for the investigation page and the export. */
function server(inv: Investigation, exportAnswer: () => Response) {
  const posts = recording(exportAnswer);

  const handler = routes({
    [`GET ${PATH}`]: () => json(200, inv),
    [`GET ${PATH}/steps`]: () => json(200, { steps: [] }),
    [`GET ${PATH}/watchers`]: () => json(200, { watchers: [] }),
    [`GET ${PATH}/feedback`]: () => json(200, { parts: [] }),
    [`POST ${PATH}/scenario-draft`]: posts.handler,
    "GET /api/v1/scenarios/latency-alert-checkout-0198f2": () => json(200, scenarioDetail()),
  });

  return { handler, posts: posts.seen };
}

beforeEach(() => {
  FakeEventSource.reset();
  vi.stubGlobal("EventSource", FakeEventSource);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Export as scenario", () => {
  it("exports a confirmed investigation and opens the draft on the review queue", async () => {
    const user = userEvent.setup();
    const { handler, posts } = server(CONFIRMED, () => json(201, scenario()));

    renderApp({ me: ADMIN, route: ROUTE, handler });

    await user.click(await screen.findByRole("button", { name: "Export as scenario" }));

    expect(await screen.findByRole("heading", { name: "Checkout latency above 2s", level: 2 })).toBeInTheDocument();
    expect(posts).toHaveLength(1);
    expect(posts[0]?.csrf).toBe(ADMIN.csrfToken);
  });

  it("shows why an export was refused", async () => {
    const user = userEvent.setup();
    const { handler } = server(CONFIRMED, () => codedProblem(422, "no_replay", "The alert that opened this investigation has no original payload to replay."));

    renderApp({ me: ADMIN, route: ROUTE, handler });

    await user.click(await screen.findByRole("button", { name: "Export as scenario" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("no original payload to replay");
  });

  it("is not offered to a responder, nor for an investigation nobody confirmed", async () => {
    const { handler } = server(CONFIRMED, () => json(201, scenario()));
    const first = renderApp({ me: RESPONDER, route: ROUTE, handler });

    await screen.findByRole("heading", { name: "Checkout p99 latency above 2s", level: 1 });
    expect(screen.queryByRole("button", { name: "Export as scenario" })).not.toBeInTheDocument();
    first.unmount();

    renderApp({ me: ADMIN, route: ROUTE, handler: server(detail(), () => json(201, scenario())).handler });

    await screen.findByRole("heading", { name: "Checkout p99 latency above 2s", level: 1 });
    expect(screen.queryByRole("button", { name: "Export as scenario" })).not.toBeInTheDocument();
  });
});
