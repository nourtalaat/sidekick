const RUN_ID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Whether value has the shape of a benchmark run ID, so a request for it can be sent. */
export function isRunId(value: string): boolean {
  return RUN_ID.test(value);
}
