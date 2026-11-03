import { ApiError, detailOf } from "./client";

/** How long the numbers of the dashboard and the benchmark lists stay fresh. The rollup behind them is recounted once a minute. */
export const STALE_MS = 30_000;

interface Result<T> {
  data?: T;
  error?: unknown;
  response: Response;
}

/** The data of an answer of the API, or an ApiError with the problem's detail. */
export function unwrap<T>(result: Result<T>): T {
  if (result.data === undefined) {
    throw new ApiError(result.response.status, detailOf(result.error, result.response.statusText));
  }

  return result.data;
}
