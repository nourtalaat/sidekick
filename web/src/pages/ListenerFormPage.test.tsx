import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { VIEWER, json, renderApp } from "../test/helpers";
import { RESPONDER, codedProblem, listener, recording, sampleListeners, validationProblem } from "../test/rules";

describe("the listener form", () => {
  it("adds a listener with only the fields that were filled in, then shows the list with a message", async () => {
    const user = userEvent.setup();
    let saved = false;

    const { handler, seen } = recording((request) => {
      if (request.method === "POST") {
        saved = true;

        return json(201, listener({ name: "billing", channel: "#billing", version: 1 }));
      }

      return json(200, { listeners: saved ? [listener({ name: "billing", channel: "#billing", version: 1 })] : [] });
    });

    renderApp({ me: RESPONDER, route: "/listeners/new", handler });

    await user.type(await screen.findByLabelText("Name"), "billing");
    await user.type(screen.getByLabelText("Channel"), "#billing");
    await user.type(screen.getByLabelText("Bot names"), "Grafana, SigNoz");
    await user.type(screen.getByLabelText("Message pattern"), "(?i)firing");
    // user.type reads [ and { as key names, so patterns are pasted.
    await user.click(screen.getByLabelText("Extract patterns"));
    await user.paste("(?P<service>[a-z]+)\nother-(?P<alert>.+)");
    await user.click(screen.getByRole("button", { name: "Add listener" }));

    expect(await screen.findByText("Listener billing saved. Every replica uses it within seconds.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Listeners", level: 1 })).toBeInTheDocument();

    const post = seen.find((r) => r.method === "POST");
    expect(post?.path).toBe("/api/v1/listeners");
    expect(post?.csrf).toBe(VIEWER.csrfToken);
    expect(post?.body).toEqual({
      name: "billing",
      channel: "#billing",
      match: { sender: { names: ["Grafana", "SigNoz"] }, text: "(?i)firing" },
      extract: ["(?P<service>[a-z]+)", "other-(?P<alert>.+)"],
    });
  });

  it("asks for a name and a channel without sending anything", async () => {
    const user = userEvent.setup();
    const { handler, seen } = recording(() => json(200, { listeners: [] }));
    renderApp({ me: RESPONDER, route: "/listeners/new", handler });

    await user.click(await screen.findByRole("button", { name: "Add listener" }));

    expect(screen.getByText("Enter a name.")).toBeInTheDocument();
    expect(screen.getByText("Enter a channel.")).toBeInTheDocument();
    expect(screen.getByLabelText("Name")).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByRole("alert")).toHaveTextContent("Not saved. Fix the fields marked below");
    expect(screen.getByRole("alert")).toHaveFocus();
    expect(seen.filter((r) => r.method === "POST")).toHaveLength(0);
  });

  it("shows each problem the server found under its field, and the others at the top", async () => {
    const user = userEvent.setup();

    const { handler } = recording((request) =>
      request.method === "POST"
        ? validationProblem([
            { field: "match.text", message: "invalid regular expression: missing closing )" },
            { field: "extract[1]", message: "has no named group" },
            { field: "", message: "listeners need the Slack app to receive events" },
            { field: "bogus", message: "is not a field" },
          ])
        : json(200, { listeners: [] }),
    );

    renderApp({ me: RESPONDER, route: "/listeners/new", handler });

    await user.type(await screen.findByLabelText("Name"), "billing");
    await user.type(screen.getByLabelText("Channel"), "#billing");
    await user.click(screen.getByRole("button", { name: "Add listener" }));

    const pattern = await screen.findByLabelText("Message pattern");
    expect(pattern).toHaveAttribute("aria-invalid", "true");
    expect(pattern).toHaveAccessibleDescription(expect.stringContaining("invalid regular expression: missing closing )"));
    expect(screen.getByLabelText("Extract patterns")).toHaveAccessibleDescription(expect.stringContaining("has no named group"));

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("listeners need the Slack app to receive events");
    expect(alert).toHaveTextContent("bogus: is not a field");
    expect(alert).toHaveFocus();
    expect(screen.getByLabelText("Name")).toHaveValue("billing");
  });

  it("says that a name is taken, on the name", async () => {
    const user = userEvent.setup();

    const { handler } = recording((request) =>
      request.method === "POST" ? codedProblem(409, "exists", "A listener named ops exists already.") : json(200, { listeners: [] }),
    );

    renderApp({ me: RESPONDER, route: "/listeners/new", handler });

    await user.type(await screen.findByLabelText("Name"), "ops");
    await user.type(screen.getByLabelText("Channel"), "#ops");
    await user.click(screen.getByRole("button", { name: "Add listener" }));

    expect(await screen.findByRole("heading", { name: "That name is taken" })).toBeInTheDocument();
    expect(screen.getByLabelText("Name")).toHaveAccessibleDescription(expect.stringContaining("A listener named ops exists already."));
  });

  it("changes a runtime listener and sends back the version it read", async () => {
    const user = userEvent.setup();

    const { handler, seen } = recording((request) =>
      request.method === "PUT" ? json(200, listener({ channel: "#ops-2", version: 3 })) : json(200, { listeners: sampleListeners() }),
    );

    renderApp({ me: RESPONDER, route: "/listeners/ops", handler });

    const channel = await screen.findByLabelText("Channel");
    expect(channel).toHaveValue("#ops");
    expect(screen.getByLabelText("Name")).toBeDisabled();
    expect(screen.getByLabelText("Bot names")).toHaveValue("Grafana");
    expect(screen.getByLabelText("Sent by")).toHaveValue("bot");

    await user.clear(channel);
    await user.type(channel, "#ops-2");
    await user.click(screen.getByLabelText("In use"));
    await user.click(screen.getByRole("button", { name: "Save listener" }));

    expect(await screen.findByText("Listener ops saved. Every replica uses it within seconds.")).toBeInTheDocument();

    const put = seen.find((r) => r.method === "PUT");
    expect(put?.path).toBe("/api/v1/listeners/ops");
    expect(put?.body).toEqual({
      channel: "#ops-2",
      match: { sender: { kind: "bot", names: ["Grafana"] }, text: "(?i)firing" },
      enabled: false,
      version: 2,
    });
  });

  it("warns that someone changed the listener, and loads the new version", async () => {
    const user = userEvent.setup();
    let current = listener({ version: 2 });

    const { handler, seen } = recording((request) =>
      request.method === "PUT" ? codedProblem(409, "stale", "The listener ops was changed since you read it.") : json(200, { listeners: [current] }),
    );

    renderApp({ me: RESPONDER, route: "/listeners/ops", handler });

    await user.click(await screen.findByRole("button", { name: "Save listener" }));

    expect(await screen.findByRole("heading", { name: "Someone changed this since you opened it" })).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveFocus();

    current = listener({ version: 3, channel: "#ops-by-someone" });
    await user.click(screen.getByRole("button", { name: "Load the new version" }));

    await waitFor(() => expect(screen.getByLabelText("Channel")).toHaveValue("#ops-by-someone"));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(seen.filter((r) => r.method === "GET").length).toBeGreaterThanOrEqual(2);
  });

  it("tells that a listener of the config file cannot be changed", async () => {
    const { handler } = recording(() => json(200, { listeners: sampleListeners().slice(0, 1) }));
    renderApp({ me: RESPONDER, route: "/listeners/alerts", handler });

    expect(await screen.findByRole("heading", { name: "No listener to change" })).toBeInTheDocument();
    expect(screen.getByText(/set in the config file/)).toBeInTheDocument();
  });

  it("is for a responder", async () => {
    renderApp({ me: VIEWER, route: "/listeners/new" });

    expect(await screen.findByRole("heading", { name: "Needs the responder role" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add listener" })).not.toBeInTheDocument();
  });
});
