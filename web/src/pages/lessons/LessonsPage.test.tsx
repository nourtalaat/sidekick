import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { Lesson } from "../../api/lessons";
import { VIEWER, json, renderApp, wcagViolations } from "../../test/helpers";
import { ADMIN, RESPONDER, codedProblem, recording, validationProblem } from "../../test/rules";

const ID = "0192d3a4-0000-7000-8000-0000000000d2";

function lesson(over: Partial<Lesson> = {}): Lesson {
  return {
    id: ID,
    text: "Say what you could not check.",
    scope: {},
    status: "active",
    origin: "ui",
    feedback: [],
    helpful: 3,
    unhelpful: 1,
    lastUsedAt: "2026-10-30T09:00:00Z",
    version: 2,
    createdAt: "2026-10-02T10:00:00Z",
    updatedAt: "2026-10-02T10:00:00Z",
    ...over,
  };
}

function list(lessons: Lesson[], extra: Record<string, unknown> = {}) {
  return json(200, { lessons, counts: { proposed: 1, active: 4, disabled: 2, rejected: 3 }, ...extra });
}

describe("the lessons tab", () => {
  it("opens on the lessons tab and says what the page is for", async () => {
    const { handler } = recording(() => list([lesson()]));
    renderApp({ me: VIEWER, route: "/lessons", handler });

    expect(await screen.findByRole("heading", { level: 1, name: "Lessons and memory" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Lessons" })).toHaveAttribute("aria-selected", "true");
    expect(await screen.findByRole("article")).toBeInTheDocument();
  });

  it("lists lessons with their status, scope, ratings and last use, as plain text", async () => {
    const { handler, seen } = recording(() =>
      list([
        lesson({ text: "<b>Check</b> the deploys & the pool.", scope: { service: "checkout-api", alert: "Latency" } }),
        lesson({ id: "x2", text: "Never used.", status: "disabled", lastUsedAt: undefined, helpful: 0, unhelpful: 0, origin: "feedback" }),
      ]),
    );

    renderApp({ me: VIEWER, route: "/lessons", handler });

    const [first, second] = await screen.findAllByRole("article");
    if (!first || !second) {
      throw new Error("want two lessons");
    }

    expect(within(first).getByText("<b>Check</b> the deploys & the pool.")).toBeInTheDocument();
    expect(first.querySelector("b")).toBeNull();
    expect(within(first).getByText("Active")).toBeInTheDocument();
    expect(within(first).getByText("service checkout-api, alert Latency")).toBeInTheDocument();
    expect(within(first).getByText("Found helpful").nextElementSibling).toHaveTextContent("3");
    expect(within(first).getByText("Found unhelpful").nextElementSibling).toHaveTextContent("1");
    expect(within(first).getByText("Last used").nextElementSibling?.querySelector("time")).not.toBeNull();

    expect(within(second).getByText("Disabled")).toBeInTheDocument();
    expect(within(second).getByText("Drafted from feedback")).toBeInTheDocument();
    expect(within(second).getByText("Last used").nextElementSibling).toHaveTextContent("Never");

    expect(screen.getByText("1 wait for review, 4 active, 2 disabled, 3 rejected")).toBeInTheDocument();

    const request = seen.find((r) => r.path === "/api/v1/lessons");
    expect(request?.search.get("status")).toBeNull();
  });

  it("sends the filters, keeps them in the address and clears them", async () => {
    const user = userEvent.setup();
    const { handler, seen } = recording(() => list([lesson()]));

    renderApp({ me: VIEWER, route: "/lessons", handler });
    await screen.findByRole("article");

    const form = screen.getByRole("form", { name: "Filter the lessons" });
    await user.selectOptions(within(form).getByRole("combobox", { name: "Status" }), "active");
    await user.type(within(form).getByRole("textbox", { name: "Service" }), "checkout-api");
    await user.type(within(form).getByRole("textbox", { name: "Words" }), "deploys");
    await user.click(within(form).getByRole("button", { name: "Filter" }));

    await vi.waitFor(() => {
      const last = seen.filter((r) => r.path === "/api/v1/lessons").at(-1);
      expect(last?.search.get("status")).toBe("active");
      expect(last?.search.get("service")).toBe("checkout-api");
      expect(last?.search.get("q")).toBe("deploys");
    });

    await user.click(screen.getByRole("button", { name: "Clear filters" }));

    await vi.waitFor(() => {
      const last = seen.filter((r) => r.path === "/api/v1/lessons").at(-1);
      expect(last?.search.get("status")).toBeNull();
      expect(last?.search.get("q")).toBeNull();
    });
  });

  it("starts from the filters in the address", async () => {
    const { handler, seen } = recording(() => list([lesson()]));
    renderApp({ me: VIEWER, route: "/lessons?tab=lessons&status=disabled&service=db&q=backup", handler });

    await screen.findByRole("article");

    const request = seen.find((r) => r.path === "/api/v1/lessons");
    expect(request?.search.get("status")).toBe("disabled");
    expect(request?.search.get("service")).toBe("db");
    expect(request?.search.get("q")).toBe("backup");
    expect(screen.getByRole("combobox", { name: "Status" })).toHaveValue("disabled");
  });

  it("says when no lesson matches the filters", async () => {
    const { handler } = recording(() => list([]));
    renderApp({ me: VIEWER, route: "/lessons?tab=lessons&q=zzz", handler });

    expect(await screen.findByRole("heading", { name: "No lessons match these filters" })).toBeInTheDocument();
  });

  it("shows older lessons with the cursor of the page before", async () => {
    const user = userEvent.setup();
    const { handler, seen } = recording((request) =>
      request.search.get("cursor") ? list([lesson({ id: "x3", text: "An older lesson." })]) : list([lesson()], { nextCursor: "next-page" }),
    );

    renderApp({ me: VIEWER, route: "/lessons", handler });

    await user.click(await screen.findByRole("button", { name: "Show older lessons" }));

    expect(await screen.findByText("An older lesson.")).toBeInTheDocument();
    expect(screen.getByText("Say what you could not check.")).toBeInTheDocument();
    expect(seen.filter((r) => r.path === "/api/v1/lessons").at(-1)?.search.get("cursor")).toBe("next-page");
    expect(screen.queryByRole("button", { name: "Show older lessons" })).not.toBeInTheDocument();
  });

  it("gives a viewer and a responder the list without the changes", async () => {
    for (const me of [VIEWER, RESPONDER]) {
      const { handler } = recording(() => list([lesson()]));
      const { unmount } = renderApp({ me, route: "/lessons", handler });

      await screen.findByRole("article");

      expect(screen.getByText(/Changing lessons needs the admin role/)).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Write a lesson" })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: /^Edit lesson/ })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: /^Disable lesson/ })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: /^Delete lesson/ })).not.toBeInTheDocument();

      unmount();
    }
  });

  it("links a lesson that waits for review to the review queue", async () => {
    const { handler } = recording(() => list([lesson({ status: "proposed", origin: "feedback", scope: { service: "db" } })]));
    renderApp({ me: ADMIN, route: "/lessons", handler });

    const article = await screen.findByRole("article");

    expect(within(article).getByText("Waits for review")).toBeInTheDocument();
    expect(within(article).getByRole("link", { name: "Review it in the review queue" })).toHaveAttribute("href", "/review?tab=lessons");
    expect(within(article).queryByRole("button", { name: /^Edit lesson/ })).not.toBeInTheDocument();
    expect(within(article).queryByRole("button", { name: /^Disable lesson/ })).not.toBeInTheDocument();
  });

  it("writes a global lesson", async () => {
    const user = userEvent.setup();
    let made = false;

    const { handler, seen } = recording((request) => {
      if (request.method === "POST") {
        made = true;

        return json(201, lesson({ id: "new", text: "Prefer short answers.", version: 1 }));
      }

      return list(made ? [lesson({ id: "new", text: "Prefer short answers.", version: 1 })] : []);
    });

    renderApp({ me: ADMIN, route: "/lessons", handler });

    await user.click(await screen.findByRole("button", { name: "Write a lesson" }));

    const form = screen.getByRole("form", { name: "Write a lesson" });
    expect(within(form).getByText(/this lesson is global and applies to every investigation/)).toBeInTheDocument();

    await user.type(within(form).getByRole("textbox", { name: "Lesson" }), "  Prefer short answers.  ");
    expect(within(form).getByText(/^25 of 400 characters/)).toBeInTheDocument();
    await user.click(within(form).getByRole("button", { name: "Save lesson" }));

    expect(await screen.findByText("Lesson saved. Prompts use it from now on.")).toBeInTheDocument();
    expect(await screen.findByText("Prefer short answers.")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Write a lesson" })).not.toBeInTheDocument();

    const post = seen.find((r) => r.method === "POST");
    expect(post?.path).toBe("/api/v1/lessons");
    expect(post?.body).toEqual({ text: "Prefer short answers.", scope: {}, status: "active" });
    expect(post?.csrf).toBe(VIEWER.csrfToken);
  });

  it("writes a lesson with a scope and shows what the server refused", async () => {
    const user = userEvent.setup();
    const { handler, seen } = recording((request) =>
      request.method === "POST" ? validationProblem([{ field: "text", message: "The text is required" }]) : list([]),
    );

    renderApp({ me: ADMIN, route: "/lessons", handler });

    await user.click(await screen.findByRole("button", { name: "Write a lesson" }));
    const form = screen.getByRole("form", { name: "Write a lesson" });
    await user.type(within(form).getByRole("textbox", { name: "Lesson" }), "x");
    await user.type(within(form).getByRole("textbox", { name: "Service" }), "db");
    expect(within(form).getByText(/It applies when every field that is filled in matches/)).toBeInTheDocument();
    await user.click(within(form).getByRole("button", { name: "Save lesson" }));

    expect(await screen.findByText("The text is required")).toBeInTheDocument();
    expect(within(form).getByRole("textbox", { name: "Lesson" })).toHaveValue("x");
    expect(seen.find((r) => r.method === "POST")?.body).toEqual({ text: "x", scope: { service: "db" }, status: "active" });
  });

  it("edits the text and scope of a lesson at the version it read", async () => {
    const user = userEvent.setup();
    const { handler, seen } = recording((request) => (request.method === "PUT" ? json(200, lesson({ version: 3 })) : list([lesson()])));

    renderApp({ me: ADMIN, route: "/lessons", handler });

    await user.click(await screen.findByRole("button", { name: /^Edit lesson/ }));

    const form = screen.getByRole("form", { name: "Edit lesson" });
    const text = within(form).getByRole("textbox", { name: "Lesson" });
    await user.clear(text);
    await user.type(text, "Be brief.");
    await user.type(within(form).getByRole("textbox", { name: "Service" }), "db");
    await user.click(within(form).getByRole("button", { name: "Save lesson" }));

    expect(await screen.findByText("Lesson saved.")).toBeInTheDocument();
    expect(seen.find((r) => r.method === "PUT")?.body).toEqual({ version: 2, text: "Be brief.", scope: { service: "db" } });
  });

  it("disables an active lesson and enables a disabled one, with their text and scope as they are", async () => {
    const user = userEvent.setup();
    let current = lesson({ scope: { service: "db" } });

    const { handler, seen } = recording((request) => {
      if (request.method === "PUT") {
        const body = request.body as { status: Lesson["status"] };
        current = { ...current, status: body.status, version: current.version + 1 };

        return json(200, current);
      }

      return list([current]);
    });

    renderApp({ me: ADMIN, route: "/lessons", handler });

    await user.click(await screen.findByRole("button", { name: /^Disable lesson/ }));
    expect(await screen.findByText("Lesson disabled. Prompts no longer use it.")).toBeInTheDocument();

    await user.click(await screen.findByRole("button", { name: /^Enable lesson/ }));
    expect(await screen.findByText("Lesson enabled. Prompts use it again.")).toBeInTheDocument();

    const puts = seen.filter((r) => r.method === "PUT");
    expect(puts[0]?.body).toEqual({ version: 2, text: "Say what you could not check.", scope: { service: "db" }, status: "disabled" });
    expect(puts[1]?.body).toEqual({ version: 3, text: "Say what you could not check.", scope: { service: "db" }, status: "active" });
  });

  it("says when someone else changed the lesson", async () => {
    const user = userEvent.setup();
    const { handler } = recording((request) => (request.method === "PUT" ? codedProblem(409, "stale", "The lesson was changed since you read it.") : list([lesson()])));

    renderApp({ me: ADMIN, route: "/lessons", handler });

    await user.click(await screen.findByRole("button", { name: /^Disable lesson/ }));

    expect(await screen.findByText("Someone changed this lesson")).toBeInTheDocument();
  });

  it("asks in the page before it deletes a lesson, and deletes only when confirmed", async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, "confirm");
    let deleted = false;

    const { handler, seen } = recording((request) => {
      if (request.method === "DELETE") {
        deleted = true;

        return new Response(null, { status: 204 });
      }

      return list(deleted ? [] : [lesson()]);
    });

    renderApp({ me: ADMIN, route: "/lessons", handler });

    await user.click(await screen.findByRole("button", { name: /^Delete lesson/ }));

    const group = screen.getByRole("group", { name: "Delete this lesson" });
    expect(within(group).getByText(/record of the investigations that used it/)).toBeInTheDocument();

    await user.click(within(group).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("group", { name: "Delete this lesson" })).not.toBeInTheDocument();
    expect(seen.some((r) => r.method === "DELETE")).toBe(false);

    await user.click(screen.getByRole("button", { name: /^Delete lesson/ }));
    await user.click(within(screen.getByRole("group", { name: "Delete this lesson" })).getByRole("button", { name: "Delete lesson" }));

    expect(await screen.findByText("Lesson deleted.")).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "No lessons yet" })).toBeInTheDocument();
    expect(seen.find((r) => r.method === "DELETE")?.path).toBe(`/api/v1/lessons/${ID}`);
    expect(confirm).not.toHaveBeenCalled();
  });

  it("has no accessibility violations", async () => {
    const { handler } = recording(() => list([lesson(), lesson({ id: "x2", status: "proposed", scope: { service: "db" } })]));
    const { container } = renderApp({ me: ADMIN, route: "/lessons", handler });

    await screen.findAllByRole("article");

    expect(await wcagViolations(container)).toEqual([]);
  });
});
