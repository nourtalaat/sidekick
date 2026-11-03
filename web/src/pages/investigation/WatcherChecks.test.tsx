import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { formatClock, formatTime } from "../../format";
import { check, checksOf, watcher } from "../../test/fixtures";
import { WatcherChecks } from "./WatcherChecks";

const STARTED = "2026-10-26T14:20:00Z";
const ENDED = "2026-10-26T14:30:00Z";

const ENDINGS = [
  {
    name: "recovered",
    watcher: watcher({ status: "recovered", startedAt: STARTED, endedAt: ENDED, checks: 3, recentChecks: checksOf(3) }),
    line: `Back up at ${formatTime(ENDED)} after 10 min`,
  },
  {
    name: "timed out",
    watcher: watcher({ status: "timed_out", startedAt: STARTED, endedAt: ENDED, checks: 3, recentChecks: checksOf(3) }),
    line: `Timed out at ${formatTime(ENDED)} after 10 min, still down`,
  },
  {
    name: "stopped by a person",
    watcher: watcher({ status: "stopped", stopReason: "responder", stoppedBy: { name: "Grace Hopper" }, startedAt: STARTED, endedAt: ENDED, checks: 3, recentChecks: checksOf(3) }),
    line: `Stopped by Grace Hopper at ${formatTime(ENDED)} after 10 min`,
  },
  {
    name: "stopped because the group was resolved",
    watcher: watcher({ status: "stopped", stopReason: "group_resolved", startedAt: STARTED, endedAt: ENDED, checks: 3, recentChecks: checksOf(3) }),
    line: `Stopped because the group was resolved at ${formatTime(ENDED)} after 10 min`,
  },
  {
    name: "stopped because the config changed",
    watcher: watcher({ status: "stopped", stopReason: "config_changed", startedAt: STARTED, endedAt: ENDED, checks: 3, recentChecks: checksOf(3) }),
    line: `Stopped because the config changed at ${formatTime(ENDED)} after 10 min`,
  },
];

describe("the checks of a watcher", () => {
  it.each(ENDINGS)("is closed for a watcher that $name, and says when it started and how it ended", async ({ watcher: w, line }) => {
    render(<WatcherChecks watcher={w} />);

    const summary = screen.getByText(/Checks \(3\)/);
    const details = summary.closest("details") as HTMLDetailsElement;

    expect(details.open).toBe(false);
    expect(summary).toHaveTextContent("Checks (3) of health");

    await userEvent.setup().click(summary);

    expect(details.open).toBe(true);
    expect(within(details).getByText(`Started ${formatTime(STARTED)}`)).toBeInTheDocument();
    expect(within(details).getByText(line)).toBeInTheDocument();
  });

  it("is open for a running watcher, which has only the started line", () => {
    const { container } = render(<WatcherChecks watcher={watcher({ checks: 2, recentChecks: checksOf(2) })} />);

    expect((container.querySelector("details") as HTMLDetailsElement).open).toBe(true);
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
    expect(screen.getByText(`Started ${formatTime(watcher().startedAt)}`)).toBeInTheDocument();
  });

  it("keeps the choice of the reader when the watcher ends", async () => {
    const running = watcher({ checks: 2, recentChecks: checksOf(2) });
    const { container, rerender } = render(<WatcherChecks watcher={running} />);
    const details = container.querySelector("details") as HTMLDetailsElement;

    await userEvent.setup().click(screen.getByText(/Checks \(2\)/));
    expect(details.open).toBe(false);

    rerender(<WatcherChecks watcher={{ ...running, status: "recovered", endedAt: ENDED }} />);
    expect(details.open).toBe(false);
    expect(screen.getByText(/Back up at/)).toBeInTheDocument();
  });

  it("lists the checks newest first with the time, the result as a word, the detail and the duration", () => {
    render(
      <WatcherChecks
        watcher={watcher({
          checks: 3,
          recentChecks: [
            check(3, { result: "pass", detail: "HTTP 200", durationMs: 90 }),
            check(2, { result: "fail", detail: "HTTP 503", durationMs: 1500 }),
            check(1, { result: "error", detail: "connection refused", durationMs: 30_000 }),
          ],
        })}
      />,
    );

    const table = screen.getByRole("table");
    const rows = within(table).getAllByRole("row").slice(1);

    expect(within(table).getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["Time", "Result", "Detail", "Duration"]);
    expect(rows.map((r) => within(r).getAllByRole("cell").map((c) => c.textContent))).toEqual([
      [formatClock(check(3).at), "Passed", "HTTP 200", "90 ms"],
      [formatClock(check(2).at), "Failed", "HTTP 503", "2 s"],
      [formatClock(check(1).at), "Error", "connection refused", "30 s"],
    ]);
    expect(screen.getByRole("region", { name: "Checks of health" })).toBeInTheDocument();
  });

  it("says when a check has no detail", () => {
    render(<WatcherChecks watcher={watcher({ checks: 1, recentChecks: [check(1, { detail: undefined })] })} />);

    expect(screen.getByText("No detail")).toBeInTheDocument();
  });

  it.each([
    { name: "more checks ran than there are rows", checks: 120, rows: 50, note: "Showing the last 50 of 120 checks." },
    { name: "every check is in the rows", checks: 3, rows: 3, note: undefined },
  ])("shows the note only when it applies: $name", ({ checks, rows, note }) => {
    render(<WatcherChecks watcher={watcher({ checks, recentChecks: checksOf(rows) })} />);

    expect(within(screen.getByRole("table")).getAllByRole("row")).toHaveLength(rows + 1);

    if (note) {
      expect(screen.getByText(note)).toBeInTheDocument();
    } else {
      expect(screen.queryByText(/^Showing the last/)).not.toBeInTheDocument();
    }
  });

  it("says that no check ran yet, without a table", () => {
    render(<WatcherChecks watcher={watcher({ checks: 0, recentChecks: [] })} />);

    expect(screen.getByText(/Checks \(0\)/)).toBeInTheDocument();
    expect(screen.getByText("No checks yet.")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("shows the detail as plain text", () => {
    const html = '<img src=x onerror="alert(1)"><b>bold</b>';
    const { container } = render(<WatcherChecks watcher={watcher({ checks: 1, recentChecks: [check(1, { detail: html })] })} />);

    expect(screen.getByText(html)).toBeInTheDocument();
    expect(container.querySelector("img, b")).toBeNull();
  });

  it.each([
    { name: "many characters", detail: "x".repeat(250) },
    { name: "many lines", detail: "a\nb\nc\nd" },
  ])("cuts a detail with $name and shows all of it on request", async ({ detail }) => {
    const user = userEvent.setup();
    const { container } = render(<WatcherChecks watcher={watcher({ checks: 1, recentChecks: [check(1, { detail })] })} />);
    const text = container.querySelector(".sk-watcher-checks__detail") as HTMLElement;

    const more = screen.getByRole("button", { name: "Show all" });
    expect(more).toHaveAttribute("aria-expanded", "false");
    expect(text).toHaveClass("sk-watcher-checks__detail--cut");
    expect(text.textContent).toBe(detail);

    await user.click(more);

    expect(screen.getByRole("button", { name: "Show less" })).toHaveAttribute("aria-expanded", "true");
    expect(text).not.toHaveClass("sk-watcher-checks__detail--cut");

    await user.click(screen.getByRole("button", { name: "Show less" }));

    expect(text).toHaveClass("sk-watcher-checks__detail--cut");
  });

  it("gives a short detail no button", () => {
    render(<WatcherChecks watcher={watcher({ checks: 1, recentChecks: [check(1, { detail: "HTTP 200" })] })} />);

    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });
});
