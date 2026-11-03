import { screen } from "@testing-library/react";
import { useEffect } from "react";
import { describe, expect, it, vi } from "vitest";
import { Route, Routes } from "react-router";
import type { Role } from "../roles";
import { RequireSession, type Me } from "../session";
import { VIEWER, fakeServer, renderWithProviders } from "../test/helpers";
import { ADMIN, RESPONDER } from "../test/rules";
import { RequireRole } from "./RequireRole";

function Child({ onMount }: { onMount: () => void }) {
  useEffect(onMount, [onMount]);

  return <p>The page</p>;
}

function renderGuarded(me: Me, role: Role, onMount: () => void) {
  const { api } = fakeServer(me);

  return renderWithProviders(
    <Routes>
      <Route element={<RequireSession />}>
        <Route
          path="/"
          element={
            <RequireRole role={role} title="Secret page">
              <Child onMount={onMount} />
            </RequireRole>
          }
        />
      </Route>
    </Routes>,
    { api },
  );
}

describe("RequireRole", () => {
  it.each([
    { name: "the same role", me: ADMIN, role: "admin" as const },
    { name: "a higher role", me: ADMIN, role: "responder" as const },
    { name: "viewer for a viewer page", me: VIEWER, role: "viewer" as const },
  ])("shows the page for $name", async ({ me, role }) => {
    const onMount = vi.fn();
    renderGuarded(me, role, onMount);

    expect(await screen.findByText("The page")).toBeInTheDocument();
    expect(onMount).toHaveBeenCalled();
  });

  it.each([
    { name: "a viewer", me: VIEWER },
    { name: "a responder", me: RESPONDER },
  ])("shows the title and the role message to $name, and does not mount the page", async ({ me }) => {
    const onMount = vi.fn();
    renderGuarded(me, "admin", onMount);

    expect(await screen.findByRole("heading", { name: "Secret page", level: 1 })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Needs the admin role" })).toBeInTheDocument();
    expect(screen.queryByText("The page")).not.toBeInTheDocument();
    expect(onMount).not.toHaveBeenCalled();
  });
});
