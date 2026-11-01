import type { components } from "../api/schema";
import { json, problem, type Handler } from "./helpers";
import { recording, type Recorded } from "./rules";

type Strategy = components["schemas"]["Strategy"];
type StrategyDetail = components["schemas"]["StrategyDetail"];
type StrategyVersionDetail = components["schemas"]["StrategyVersionDetail"];

export const CHECKOUT_V1 = "name: checkout-latency\ndescription: Checkout is slow.\n";
export const CHECKOUT_V2 = "name: checkout-latency\ndescription: Checkout is slow.\nfirst:\n  - Check what changed.\n";
export const GENERIC_BODY = "name: generic-alert\ndescription: Any alert.\nfirst:\n  - Read the alert.\n";

export function strategy(over: Partial<Strategy> = {}): Strategy {
  return {
    name: "checkout-latency",
    description: "Checkout is slow.",
    layer: "runtime",
    status: "active",
    readOnly: false,
    version: "runtime:2",
    runtimeVersion: 2,
    updatedBy: "Ad",
    updatedAt: "2026-10-21T10:00:00Z",
    ...over,
  };
}

const NO_RUNTIME = { runtimeVersion: undefined, updatedBy: undefined, updatedAt: undefined } as const;

/** A built-in strategy. */
export function builtIn(over: Partial<Strategy> = {}): Strategy {
  return strategy({ name: "generic-alert", description: "Any alert.", layer: "built-in", readOnly: true, version: "0123456789abcdef", ...NO_RUNTIME, ...over });
}

/** A strategy file, which replaces the runtime strategy of its name. */
export function fileStrategy(over: Partial<Strategy> = {}): Strategy {
  return strategy({ name: "file-latency", description: "From a file.", layer: "file", readOnly: true, version: "fedcba9876543210", overrides: ["runtime"], ...NO_RUNTIME, ...over });
}

/** The list of the strategy tests: a runtime one, a built-in one, a file one that shadows a runtime one, and a broken runtime one. */
export function sampleStrategies(): Strategy[] {
  return [
    strategy(),
    builtIn(),
    fileStrategy(),
    strategy({
      name: "file-latency",
      status: "shadowed",
      statusReason: "a strategy file has the same name, and is used instead",
      version: "runtime:1",
      runtimeVersion: 1,
    }),
    strategy({ name: "broken", description: undefined, status: "invalid", statusReason: "description: required", version: "runtime:1", runtimeVersion: 1 }),
  ];
}

export function version(over: Partial<StrategyVersionDetail> = {}): StrategyVersionDetail {
  return {
    name: "checkout-latency",
    version: 2,
    reason: "Add the first check",
    authorName: "Ad",
    createdAt: "2026-10-21T10:00:00Z",
    current: true,
    body: CHECKOUT_V2,
    ...over,
  };
}

/** The two versions of checkout-latency, newest first. */
export function checkoutVersions(): StrategyVersionDetail[] {
  return [version(), version({ version: 1, reason: "First draft", createdAt: "2026-10-20T10:00:00Z", current: false, body: CHECKOUT_V1 })];
}

interface Content {
  list: Strategy[];
  details: Record<string, StrategyDetail>;
  versions: Record<string, StrategyVersionDetail[]>;
  /** Answers a save; the default adds the next version. */
  save?: (request: Recorded) => Response;
}

/** A fake server of the strategy endpoints over content, and the requests it saw. */
export function strategyApi(content: Content): { handler: Handler; seen: Recorded[] } {
  return recording((request) => {
    const path = request.path.replace("/api/v1", "");

    if (path === "/strategies") {
      return json(200, { strategies: content.list });
    }

    const one = /^\/strategies\/([^/]+)$/.exec(path);
    if (one) {
      const found = content.details[decodeURIComponent(one[1] ?? "")];

      return found ? json(200, found) : problem(404, "No strategy has this name.");
    }

    const many = /^\/strategies\/([^/]+)\/versions$/.exec(path);
    if (many) {
      const name = decodeURIComponent(many[1] ?? "");

      if (request.method === "POST") {
        return (content.save ?? ((r) => saveNext(content, name, r)))(request);
      }

      const listed = (content.versions[name] ?? []).map((v) => ({
        name: v.name,
        version: v.version,
        reason: v.reason,
        authorName: v.authorName,
        createdAt: v.createdAt,
        current: v.current,
      }));

      return json(200, { versions: listed });
    }

    const single = /^\/strategies\/([^/]+)\/versions\/(\d+)$/.exec(path);
    if (single) {
      const found = (content.versions[decodeURIComponent(single[1] ?? "")] ?? []).find((v) => v.version === Number(single[2]));

      return found ? json(200, found) : problem(404, "No such version.");
    }

    return problem(404, "Not found.");
  });
}

/** Adds the body of a save as the next version, as the server does. */
function saveNext(content: Content, name: string, request: Recorded): Response {
  const body = request.body as { body: string; reason: string };
  const old = content.versions[name] ?? [];
  const saved = version({ name, version: (old[0]?.version ?? 0) + 1, reason: body.reason, body: body.body, current: true });

  content.versions[name] = [saved, ...old.map((v) => ({ ...v, current: false }))];

  return json(201, saved);
}

/** The content for checkout-latency as a runtime strategy with two versions, and the built-in generic-alert. */
export function sampleContent(): Content {
  return {
    list: sampleStrategies(),
    details: {
      "checkout-latency": { ...strategy(), body: CHECKOUT_V2 },
      "generic-alert": { ...builtIn(), body: GENERIC_BODY },
      "file-latency": { ...fileStrategy(), body: "name: file-latency\ndescription: From a file.\n" },
    },
    versions: { "checkout-latency": checkoutVersions() },
  };
}
