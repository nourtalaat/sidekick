import type { InfiniteData } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import { withoutJob, type Job, type JobList } from "./jobs";

function job(id: number): Job {
  return { id, kind: "publish", attempts: 5, maxAttempts: 5, createdAt: "2026-10-27T08:00:00Z" };
}

function pagesOf(...pages: number[][]): InfiniteData<JobList, string | undefined> {
  return {
    pages: pages.map((ids, i) => ({ jobs: ids.map(job), nextCursor: i < pages.length - 1 ? `cursor-${i}` : undefined })),
    pageParams: pages.map((_, i) => (i === 0 ? undefined : `cursor-${i - 1}`)),
  };
}

describe("withoutJob", () => {
  it("takes the job out of the page that holds it and keeps every other page and cursor", () => {
    const data = withoutJob(pagesOf([3, 2], [1]), 2);

    expect(data.pages.map((page) => page.jobs.map((j) => j.id))).toEqual([[3], [1]]);
    expect(data.pages.map((page) => page.nextCursor)).toEqual(["cursor-0", undefined]);
    expect(data.pageParams).toEqual([undefined, "cursor-0"]);
  });

  it("keeps an emptied page, so the cursor to the next one is not lost", () => {
    const data = withoutJob(pagesOf([3], [2, 1]), 3);

    expect(data.pages[0]?.jobs).toEqual([]);
    expect(data.pages[0]?.nextCursor).toBe("cursor-0");
  });

  it("changes nothing for a job that is not in the pages", () => {
    expect(withoutJob(pagesOf([3, 2]), 99)).toEqual(pagesOf([3, 2]));
  });
});
