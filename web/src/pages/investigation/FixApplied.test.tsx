import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { detail, INVESTIGATION_ID } from "../../test/fixtures";
import { fakeServer, json, problem, renderWithProviders, VIEWER } from "../../test/helpers";
import { routes } from "../../test/streams";
import { FixApplied } from "./FixApplied";

const PATH = `/api/v1/investigations/${INVESTIGATION_ID}/fix-applied`;

const FALSE_POSITIVE = detail({ verdict: "false_positive" });
const APPLIED = detail({ verdict: "false_positive", fixAppliedAt: "2026-10-26T15:00:00Z" });

/** A fake server that records the methods sent to the fix-applied path. */
function server(answer: () => Response = () => json(200, { applied: true, changed: true, appliedAt: "2026-10-26T15:00:00Z" })) {
  const sent: { method: string; csrf: string | null }[] = [];

  const { api } = fakeServer(
    VIEWER,
    routes({
      [`PUT ${PATH}`]: (request) => {
        sent.push({ method: request.method, csrf: request.headers.get("X-CSRF-Token") });

        return answer();
      },
      [`DELETE ${PATH}`]: (request) => {
        sent.push({ method: request.method, csrf: request.headers.get("X-CSRF-Token") });

        return json(200, { applied: false, changed: true });
      },
    }),
  );

  return { api, sent };
}

describe("FixApplied", () => {
  it("shows nothing for an investigation that is not a false positive", () => {
    const { container } = renderWithProviders(<FixApplied inv={detail()} canAct />);

    expect(container).toBeEmptyDOMElement();
  });

  it("shows nothing while the investigation runs", () => {
    const { container } = renderWithProviders(<FixApplied inv={detail({ verdict: "false_positive", status: "running" })} canAct />);

    expect(container).toBeEmptyDOMElement();
  });

  it("lets a responder mark the fix as applied", async () => {
    const user = userEvent.setup();
    const { api, sent } = server();
    renderWithProviders(<FixApplied inv={FALSE_POSITIVE} canAct />, { api });

    expect(screen.getByText("Fix not applied yet")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Mark fix as applied" }));

    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]?.method).toBe("PUT");
  });

  it("shows when the fix was marked and lets a responder take the mark back", async () => {
    const user = userEvent.setup();
    const { api, sent } = server();
    renderWithProviders(<FixApplied inv={APPLIED} canAct />, { api });

    expect(screen.getByText("Fix applied")).toBeInTheDocument();
    expect(screen.getByText(/^Marked /)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Take back the mark" }));

    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]?.method).toBe("DELETE");
  });

  it("gives a viewer the state and no button", () => {
    renderWithProviders(<FixApplied inv={APPLIED} canAct={false} />);

    expect(screen.getByText("Fix applied")).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("says why the mark failed", async () => {
    const user = userEvent.setup();
    const { api } = server(() => problem(409, "Only an investigation that ended as a false positive has a fix to mark as applied."));
    renderWithProviders(<FixApplied inv={FALSE_POSITIVE} canAct />, { api });

    await user.click(screen.getByRole("button", { name: "Mark fix as applied" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("Only an investigation that ended as a false positive");
  });
});
