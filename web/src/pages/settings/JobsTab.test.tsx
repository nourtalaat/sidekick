import { QueryClient } from "@tanstack/react-query";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { Job, JobList } from "../../api/jobs";
import { sampleConfig } from "../../test/config";
import { json, problem, renderApp } from "../../test/helpers";
import { ADMIN, recording, rowAt, type Recorded } from "../../test/rules";

const ROUTE = "/settings?tab=failed-jobs";
const JOBS_PATH = "/api/v1/jobs";
const NOT_DEAD = "The job is pending, not dead; only dead jobs can be retried.";
const SAME_KEY = "Another job with the same unique key is pending or running.";

function job(id: number, over: Partial<Job> = {}): Job {
  return {
    id,
    kind: "publish",
    route: "default",
    attempts: 5,
    maxAttempts: 5,
    lastError: "slack: not allowed to post",
    createdAt: "2026-10-27T08:00:00Z",
    finishedAt: "2026-10-27T09:00:00Z",
    ...over,
  };
}

interface FakeOptions {
  /** The pages by cursor; the first page has the cursor "". */
  pages: Record<string, JobList>;
  retry?: (id: number) => Response;
  discard?: (id: number) => Response;
}

/** A fake of the job calls. A job that was retried or discarded is left out of the pages from then on. */
function fakeJobs(options: FakeOptions) {
  const gone = new Set<number>();

  const { handler, seen } = recording((request: Recorded) => {
    if (request.path === "/api/v1/config") {
      return json(200, sampleConfig());
    }

    if (request.path === JOBS_PATH && request.method === "GET") {
      const page = options.pages[request.search.get("cursor") ?? ""];

      return page ? json(200, { ...page, jobs: page.jobs.filter((j) => !gone.has(j.id)) }) : problem(400, "cursor is not one this API returned.");
    }

    const action = /^\/api\/v1\/jobs\/(\d+)\/(retry|discard)$/.exec(request.path);
    if (action && request.method === "POST") {
      const id = Number(action[1]);
      const answer = (action[2] === "retry" ? options.retry : options.discard)?.(id);
      if (answer) {
        return answer;
      }

      gone.add(id);

      return new Response(null, { status: 204 });
    }

    return problem(404, "Not found.");
  });

  return { handler, seen };
}

function open(options: FakeOptions, route = ROUTE, queryClient?: QueryClient) {
  const { handler, seen } = fakeJobs(options);

  return { ...renderApp({ me: ADMIN, route, handler, queryClient }), requests: seen };
}

function onePage(...jobs: Job[]): FakeOptions {
  return { pages: { "": { jobs } } };
}

function rows(): HTMLElement[] {
  return within(screen.getByRole("region", { name: "Failed jobs" })).getAllByRole("row").slice(1);
}

function kindsAndIds(): string[] {
  return screen.getAllByRole("button", { name: /^Retry / }).map((button) => button.getAttribute("aria-label") ?? "");
}

function gets(requests: Recorded[]): Recorded[] {
  return requests.filter((r) => r.method === "GET" && r.path === JOBS_PATH);
}

function posts(requests: Recorded[]): Recorded[] {
  return requests.filter((r) => r.method === "POST");
}

describe("the Failed jobs tab: the list", () => {
  it("is the last tab of the Settings page, has no count in its label, and sends no job request until it opens", async () => {
    const user = userEvent.setup();
    const { requests } = open(onePage(job(1)), "/settings");

    expect(await screen.findByRole("tab", { name: "Config" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getAllByRole("tab").map((t) => t.textContent)).toEqual(["Config", "API tokens", "Failed jobs"]);
    expect(requests.filter((r) => r.path === JOBS_PATH)).toEqual([]);

    await user.click(screen.getByRole("tab", { name: "Failed jobs" }));

    expect(await screen.findByRole("region", { name: "Failed jobs" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Failed jobs" })).toHaveAttribute("aria-selected", "true");
    expect(gets(requests)).toHaveLength(1);
    expect(gets(requests)[0]?.search.get("status")).toBe("dead");
  });

  it("shows each job with its kind, route, attempts, times and last error, in the order the API gives", async () => {
    open(onePage(job(3, { kind: "investigate", route: undefined, attempts: 1, lastError: "decode payload: unexpected end of JSON input" }), job(2, { route: "checkout" })));
    await screen.findByRole("region", { name: "Failed jobs" });

    expect(screen.getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["Kind", "Route", "Attempts", "Created", "Died", "Last error", "Actions"]);

    const [first, second] = [rowAt(rows(), 0), rowAt(rows(), 1)];
    expect(within(first).getByRole("rowheader")).toHaveTextContent("investigate");
    expect(within(first).getAllByRole("cell").map((c) => c.textContent)).toEqual(
      expect.arrayContaining(["none", "1 of 5", "decode payload: unexpected end of JSON input"]),
    );
    expect(within(second).getByText("checkout")).toBeInTheDocument();
    expect(within(second).getByText("5 of 5")).toBeInTheDocument();
    expect(within(second).getByText(new Date("2026-10-27T08:00:00Z").toLocaleString()).tagName).toBe("TIME");
    expect(within(second).getByText(new Date("2026-10-27T09:00:00Z").toLocaleString())).toHaveAttribute("datetime", "2026-10-27T09:00:00Z");
  });

  it("says No failed jobs when the list is empty", async () => {
    open(onePage());

    expect(await screen.findByRole("heading", { name: "No failed jobs." })).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("says why the list could not be read, and reads it again on request", async () => {
    const user = userEvent.setup();
    let fail = true;
    const { handler, seen } = recording((request: Recorded) => {
      if (request.path === JOBS_PATH && fail) {
        return problem(500, "The database is down.");
      }

      return json(200, { jobs: [job(1)] });
    });
    renderApp({ me: ADMIN, route: ROUTE, handler, queryClient: new QueryClient({ defaultOptions: { queries: { retry: false } } }) });

    expect(await screen.findByText("The database is down.")).toBeInTheDocument();

    fail = false;
    await user.click(screen.getByRole("button", { name: "Try again" }));

    expect(await screen.findByRole("region", { name: "Failed jobs" })).toBeInTheDocument();
    expect(seen.filter((r) => r.path === JOBS_PATH)).toHaveLength(2);
  });

  it("never shows the payload of a job", async () => {
    const withPayload = { ...job(1), payload: { token: "do-not-show-me" } } as Job;
    open(onePage(withPayload));
    await screen.findByRole("region", { name: "Failed jobs" });

    expect(document.body).not.toHaveTextContent("do-not-show-me");
  });
});

describe("the Failed jobs tab: Load more", () => {
  const PAGES: FakeOptions = {
    pages: {
      "": { jobs: [job(3), job(2)], nextCursor: "after-2" },
      "after-2": { jobs: [job(1)] },
    },
  };

  it("asks for the next page with the cursor of the last one, adds its rows and then hides the button", async () => {
    const user = userEvent.setup();
    const { requests } = open(PAGES);
    await screen.findByRole("region", { name: "Failed jobs" });

    expect(kindsAndIds()).toEqual(["Retry publish job 3", "Retry publish job 2"]);

    await user.click(screen.getByRole("button", { name: "Load more" }));

    expect(await screen.findByRole("button", { name: "Retry publish job 1" })).toBeInTheDocument();
    expect(kindsAndIds()).toEqual(["Retry publish job 3", "Retry publish job 2", "Retry publish job 1"]);
    expect(gets(requests).map((r) => r.search.get("cursor"))).toEqual([null, "after-2"]);
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("keeps the rows already shown, and the way to older ones, when every row of the pages read is retried", async () => {
    const user = userEvent.setup();
    open({ pages: { "": { jobs: [job(3)], nextCursor: "after-3" }, "after-3": { jobs: [job(2)] } } });
    await screen.findByRole("region", { name: "Failed jobs" });

    await user.click(screen.getByRole("button", { name: "Retry publish job 3" }));

    expect(await screen.findByText(/No failed jobs are left in the pages read so far/)).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "No failed jobs." })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Load more" }));

    expect(await screen.findByRole("button", { name: "Retry publish job 2" })).toBeInTheDocument();
  });
});

describe("the Failed jobs tab: Retry", () => {
  it("sends the retry with the CSRF token, removes the row without reading the list again, and keeps the other pages", async () => {
    const user = userEvent.setup();
    const { requests } = open({ pages: { "": { jobs: [job(3), job(2)], nextCursor: "after-2" }, "after-2": { jobs: [job(1)] } } });
    await screen.findByRole("region", { name: "Failed jobs" });
    await user.click(screen.getByRole("button", { name: "Load more" }));
    await screen.findByRole("button", { name: "Retry publish job 1" });

    await user.click(screen.getByRole("button", { name: "Retry publish job 2" }));

    const notice = await screen.findByRole("status");
    expect(notice).toHaveTextContent("The publish job 2 is pending again.");
    expect(notice).toHaveFocus();
    expect(kindsAndIds()).toEqual(["Retry publish job 3", "Retry publish job 1"]);
    expect(posts(requests)).toEqual([expect.objectContaining({ path: "/api/v1/jobs/2/retry", csrf: "csrf-ada" })]);
    expect(gets(requests)).toHaveLength(2);
  });

  it("shows the reason of a 409 on the row, and reads the list again; a job that stays dead keeps its row", async () => {
    const user = userEvent.setup();
    const { requests } = open({ ...onePage(job(5), job(4)), retry: (id) => (id === 5 ? problem(409, SAME_KEY) : new Response(null, { status: 204 })) });
    await screen.findByRole("region", { name: "Failed jobs" });

    await user.click(screen.getByRole("button", { name: "Retry publish job 5" }));

    const alert = await within(rowAt(rows(), 0)).findByRole("alert");
    expect(alert).toHaveTextContent(`Not retried. ${SAME_KEY}`);
    expect(within(rowAt(rows(), 1)).queryByRole("alert")).not.toBeInTheDocument();
    expect(kindsAndIds()).toEqual(["Retry publish job 5", "Retry publish job 4"]);
    expect(gets(requests)).toHaveLength(2);
  });

  it("shows the reason above the list when the job left it, so the reason is not lost with the row", async () => {
    const user = userEvent.setup();
    const { handler, seen } = recording((request: Recorded) => {
      if (request.method === "POST") {
        return problem(409, NOT_DEAD);
      }

      return json(200, { jobs: seen.filter((r) => r.method === "GET" && r.path === JOBS_PATH).length === 1 ? [job(5), job(4)] : [job(4)] });
    });
    renderApp({ me: ADMIN, route: ROUTE, handler });
    await screen.findByRole("region", { name: "Failed jobs" });

    await user.click(screen.getByRole("button", { name: "Retry publish job 5" }));

    const title = await screen.findByRole("heading", { name: "The publish job 5 was not retried" });
    expect(title.closest('[role="alert"]')).toHaveTextContent(NOT_DEAD);
    expect(kindsAndIds()).toEqual(["Retry publish job 4"]);
  });
});

describe("the Failed jobs tab: Discard", () => {
  it("asks first, says what the job was, and sends nothing until it is confirmed", async () => {
    const user = userEvent.setup();
    const { requests } = open(onePage(job(2, { kind: "investigate", route: "checkout", attempts: 4, maxAttempts: 5 })));
    await screen.findByRole("region", { name: "Failed jobs" });

    await user.click(screen.getByRole("button", { name: "Discard investigate job 2" }));

    const confirm = screen.getByRole("group", { name: "Discard this investigate job" });
    expect(confirm).toHaveTextContent("investigate job 2, route checkout, 4 of 5 attempts.");
    expect(confirm).toHaveTextContent("It will not run again. This cannot be undone.");
    expect(within(confirm).getByRole("button", { name: "Cancel" })).toHaveFocus();
    expect(posts(requests)).toEqual([]);
  });

  it("closes the question on Cancel, returns focus to Discard, and sends nothing", async () => {
    const user = userEvent.setup();
    const { requests } = open(onePage(job(2)));
    await screen.findByRole("region", { name: "Failed jobs" });
    await user.click(screen.getByRole("button", { name: "Discard publish job 2" }));

    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(screen.queryByRole("group", { name: "Discard this publish job" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Discard publish job 2" })).toHaveFocus();
    expect(posts(requests)).toEqual([]);
  });

  it("sends the discard with the CSRF token once it is confirmed, and removes the row", async () => {
    const user = userEvent.setup();
    const { requests } = open(onePage(job(3), job(2)));
    await screen.findByRole("region", { name: "Failed jobs" });
    await user.click(screen.getByRole("button", { name: "Discard publish job 2" }));

    await user.click(screen.getByRole("button", { name: "Discard publish job" }));

    const notice = await screen.findByRole("status");
    expect(notice).toHaveTextContent("The publish job 2 is deleted and will not run again.");
    expect(kindsAndIds()).toEqual(["Retry publish job 3"]);
    expect(screen.queryByRole("group")).not.toBeInTheDocument();
    expect(posts(requests)).toEqual([expect.objectContaining({ path: "/api/v1/jobs/2/discard", csrf: "csrf-ada" })]);
    expect(gets(requests)).toHaveLength(1);
  });

  it("shows the reason of a 409 on the row, closes the question and reads the list again", async () => {
    const user = userEvent.setup();
    const reason = "The job is running, not dead; only dead jobs can be discarded.";
    const { requests } = open({ ...onePage(job(2)), discard: () => problem(409, reason) });
    await screen.findByRole("region", { name: "Failed jobs" });
    await user.click(screen.getByRole("button", { name: "Discard publish job 2" }));

    await user.click(screen.getByRole("button", { name: "Discard publish job" }));

    expect(await within(rowAt(rows(), 0)).findByRole("alert")).toHaveTextContent(`Not discarded. ${reason}`);
    expect(screen.queryByRole("group")).not.toBeInTheDocument();
    expect(gets(requests)).toHaveLength(2);
  });
});

describe("the Failed jobs tab: the last error", () => {
  const MARKUP = `<img src=x onerror="alert(1)"><b>boom</b>`;

  it("is plain text, so markup in it is shown as it is and is not rendered", async () => {
    open(onePage(job(1, { lastError: MARKUP })));
    await screen.findByRole("region", { name: "Failed jobs" });

    expect(screen.getByText(MARKUP)).toBeInTheDocument();
    expect(document.querySelector("tbody img, tbody b")).toBeNull();
  });

  it("has no button to show more for a short error, and none for a job without one", async () => {
    open(onePage(job(1), job(2, { lastError: undefined })));
    await screen.findByRole("region", { name: "Failed jobs" });

    expect(screen.queryByRole("button", { name: /Show/ })).not.toBeInTheDocument();
    expect(screen.getByText("No error recorded")).toBeInTheDocument();
  });

  it("cuts a long error to a few lines, and a button the keyboard reaches shows and hides the whole text", async () => {
    const user = userEvent.setup();
    const whole = ["first line", "second line", "third line", "fourth line", `fifth ${MARKUP}`].join("\n");
    open(onePage(job(1, { lastError: whole })));
    await screen.findByRole("region", { name: "Failed jobs" });
    const error = () => rowAt(rows(), 0).querySelector("pre");

    expect(error()?.textContent).toBe("first line\nsecond line\nthird line…");

    const button = screen.getByRole("button", { name: "Show all of the last error of job 1" });
    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(button).toHaveAttribute("aria-controls", error()?.id);

    button.focus();
    await user.keyboard("{Enter}");

    expect(error()?.textContent).toBe(whole);
    expect(document.querySelector("tbody img, tbody b")).toBeNull();
    expect(screen.getByRole("button", { name: "Show less of the last error of job 1" })).toHaveAttribute("aria-expanded", "true");

    await user.keyboard(" ");

    expect(error()?.textContent).toBe("first line\nsecond line\nthird line…");
  });
});
