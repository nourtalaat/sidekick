import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { Lesson } from "../../api/lessons";
import { VIEWER, json, renderApp, wcagViolations } from "../../test/helpers";
import { ADMIN, RESPONDER, codedProblem, recording, validationProblem } from "../../test/rules";

function lesson(over: Partial<Lesson> = {}): Lesson {
  return {
    id: "0192d3a4-0000-7000-8000-0000000000d1",
    text: "Search the service the alert names, not the route.",
    scope: { service: "checkout-api" },
    status: "proposed",
    origin: "feedback",
    feedback: [{ feedbackId: "f1", investigationId: "0192d3a4-0000-7000-8000-0000000000c2" }],
    helpful: 0,
    unhelpful: 0,
    version: 1,
    createdAt: "2026-10-26T10:00:00Z",
    updatedAt: "2026-10-26T10:00:00Z",
    ...over,
  };
}

function list(lessons: Lesson[], extra: Record<string, unknown> = {}) {
  return json(200, { lessons, counts: { proposed: lessons.length, active: 3, disabled: 0, rejected: 1 }, ...extra });
}

describe("the review queue", () => {
  it("opens on the lessons tab, also when the address names no tab or an unknown one", async () => {
    for (const route of ["/review", "/review?tab=lessons", "/review?tab=nonsense"]) {
      const { handler } = recording(() => list([lesson()]));
      const { unmount } = renderApp({ me: VIEWER, route, handler });

      const tab = await screen.findByRole("tab", { name: "Lessons" });
      expect(tab).toHaveAttribute("aria-selected", "true");
      expect(screen.getByRole("tabpanel")).toHaveAccessibleName("Lessons");
      expect(screen.getByRole("heading", { level: 1, name: "Review queue" })).toBeInTheDocument();

      unmount();
    }
  });

  it("lists the lessons that wait, with their text as plain text, scope and the feedback they come from", async () => {
    const { handler, seen } = recording(() => list([lesson({ text: "<b>Check</b> the deploys & the pool." })]));
    renderApp({ me: VIEWER, route: "/review", handler });

    const article = await screen.findByRole("article");

    expect(within(article).getByText("<b>Check</b> the deploys & the pool.")).toBeInTheDocument();
    expect(article.querySelector("b")).toBeNull();
    expect(within(article).getByText("service checkout-api")).toBeInTheDocument();
    expect(within(article).getByRole("link", { name: "investigation 000000c2" })).toHaveAttribute("href", "/investigations/0192d3a4-0000-7000-8000-0000000000c2");
    expect(screen.getByText(/1 lesson waits for review/)).toBeInTheDocument();

    const request = seen.find((r) => r.path === "/api/v1/lessons");
    expect(request?.search.get("status")).toBe("proposed");
  });

  it("shows a global lesson as applying to every investigation", async () => {
    const { handler } = recording(() => list([lesson({ scope: {}, origin: "ui" })]));
    renderApp({ me: ADMIN, route: "/review", handler });

    expect(await screen.findByText("every investigation")).toBeInTheDocument();
  });

  it("says when nothing waits", async () => {
    const { handler } = recording(() => list([]));
    renderApp({ me: ADMIN, route: "/review", handler });

    expect(await screen.findByRole("heading", { name: "No lessons wait for review" })).toBeInTheDocument();
  });

  it("gives a viewer and a responder the list without the choices", async () => {
    for (const me of [VIEWER, RESPONDER]) {
      const { handler } = recording(() => list([lesson()]));
      const { unmount } = renderApp({ me, route: "/review", handler });

      await screen.findByRole("article");

      expect(screen.getByText("Approving, editing and rejecting lessons needs the admin role.")).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Reject" })).not.toBeInTheDocument();

      unmount();
    }
  });

  it("approves a lesson at the version it read, with the CSRF token", async () => {
    const user = userEvent.setup();
    let waiting = [lesson({ version: 3 })];

    const { handler, seen } = recording((request) => {
      if (request.method === "POST") {
        waiting = [];

        return json(200, lesson({ status: "active", version: 4 }));
      }

      return list(waiting);
    });

    renderApp({ me: ADMIN, route: "/review", handler });

    await user.click(await screen.findByRole("button", { name: "Approve" }));

    expect(await screen.findByText("Lesson approved. Prompts use it from now on.")).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "No lessons wait for review" })).toBeInTheDocument();

    const post = seen.find((r) => r.method === "POST");
    expect(post?.path).toBe("/api/v1/lessons/0192d3a4-0000-7000-8000-0000000000d1/approve");
    expect(post?.body).toEqual({ version: 3 });
    expect(post?.csrf).toBe(VIEWER.csrfToken);
  });

  it("rejects a lesson with its text and scope as they are", async () => {
    const user = userEvent.setup();
    let waiting = [lesson()];

    const { handler, seen } = recording((request) => {
      if (request.method === "PUT") {
        waiting = [];

        return json(200, lesson({ status: "rejected", version: 2 }));
      }

      return list(waiting);
    });

    renderApp({ me: ADMIN, route: "/review", handler });

    await user.click(await screen.findByRole("button", { name: "Reject" }));

    expect(await screen.findByText("Lesson rejected.")).toBeInTheDocument();

    const put = seen.find((r) => r.method === "PUT");
    expect(put?.body).toEqual({
      version: 1,
      text: "Search the service the alert names, not the route.",
      scope: { service: "checkout-api" },
      status: "rejected",
    });
  });

  it("edits a lesson and keeps it waiting", async () => {
    const user = userEvent.setup();

    const { handler, seen } = recording((request) => (request.method === "PUT" ? json(200, lesson({ version: 2 })) : list([lesson()])));
    renderApp({ me: ADMIN, route: "/review", handler });

    await user.click(await screen.findByRole("button", { name: "Edit" }));

    const form = screen.getByRole("form", { name: "Edit lesson" });
    const text = within(form).getByRole("textbox", { name: "Lesson" });

    expect(text).toHaveValue("Search the service the alert names, not the route.");
    expect(within(form).getByText("50 of 400 characters")).toBeInTheDocument();

    await user.clear(text);
    await user.type(text, "Search the service.");
    await user.type(within(form).getByRole("textbox", { name: "Route" }), "payments");
    await user.click(within(form).getByRole("button", { name: "Save lesson" }));

    expect(await screen.findByText("Lesson saved.")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Edit lesson" })).not.toBeInTheDocument();

    const put = seen.find((r) => r.method === "PUT");
    expect(put?.body).toEqual({ version: 1, text: "Search the service.", scope: { service: "checkout-api", route: "payments" } });
  });

  it("shows each problem the server found under its field and keeps what was typed", async () => {
    const user = userEvent.setup();

    const { handler } = recording((request) =>
      request.method === "PUT"
        ? validationProblem([
            { field: "text", message: "The text is required" },
            { field: "scope", message: "A lesson drafted from feedback needs at least one scope field" },
          ])
        : list([lesson()]),
    );

    renderApp({ me: ADMIN, route: "/review", handler });

    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.clear(screen.getByRole("textbox", { name: "Service" }));
    await user.click(screen.getByRole("button", { name: "Save lesson" }));

    expect(await screen.findByText("The text is required")).toBeInTheDocument();
    expect(screen.getByText("scope: A lesson drafted from feedback needs at least one scope field")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Lesson" })).toHaveValue("Search the service the alert names, not the route.");
  });

  it("says when someone else changed the lesson, and stops the save", async () => {
    const user = userEvent.setup();

    const { handler } = recording((request) =>
      request.method === "PUT" ? codedProblem(409, "stale", "The lesson was changed since you read it.") : list([lesson()]),
    );

    renderApp({ me: ADMIN, route: "/review", handler });

    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("button", { name: "Save lesson" }));

    expect(await screen.findByText("Someone changed this lesson")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save lesson" })).toBeDisabled();
  });

  it("says why an approval was refused", async () => {
    const user = userEvent.setup();

    const { handler } = recording((request) =>
      request.method === "POST" ? codedProblem(409, "not_proposed", "The lesson does not wait for review.") : list([lesson()]),
    );

    renderApp({ me: ADMIN, route: "/review", handler });

    await user.click(await screen.findByRole("button", { name: "Approve" }));

    expect(await screen.findByText("Could not save the decision")).toBeInTheDocument();
    expect(screen.getByText("The lesson does not wait for review.")).toBeInTheDocument();
  });

  it("has no accessibility violations", async () => {
    const { handler } = recording(() => list([lesson(), lesson({ id: "0192d3a4-0000-7000-8000-0000000000d2", scope: {} })]));
    const { container } = renderApp({ me: ADMIN, route: "/review", handler });

    await screen.findAllByRole("article");

    expect(await wcagViolations(container)).toEqual([]);
  });
});
