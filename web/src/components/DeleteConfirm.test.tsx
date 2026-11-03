import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { DeleteConfirm } from "./DeleteConfirm";

describe("DeleteConfirm", () => {
  it("asks in the page, names what is deleted and starts on Cancel", () => {
    const confirm = vi.spyOn(window, "confirm");

    render(
      <DeleteConfirm subject="lesson" onConfirm={() => undefined} onCancel={() => undefined}>
        <p>It is gone for good.</p>
      </DeleteConfirm>,
    );

    expect(screen.getByRole("group", { name: "Delete this lesson" })).toBeInTheDocument();
    expect(screen.getByText("Delete this lesson?")).toBeInTheDocument();
    expect(screen.getByText("It is gone for good.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cancel" })).toHaveFocus();
    expect(confirm).not.toHaveBeenCalled();
  });

  it("deletes only when the destructive button is pressed", async () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();

    render(<DeleteConfirm subject="memory entry" onConfirm={onConfirm} onCancel={onCancel} />);

    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onCancel).toHaveBeenCalledOnce();
    expect(onConfirm).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: "Delete memory entry" }));
    expect(onConfirm).toHaveBeenCalledOnce();
  });

  it("can ask about plural things", () => {
    render(<DeleteConfirm subject="notes" determiner="these" onConfirm={() => undefined} onCancel={() => undefined} />);

    expect(screen.getByRole("group", { name: "Delete these notes" })).toBeInTheDocument();
    expect(screen.getByText("Delete these notes?")).toBeInTheDocument();
  });

  it("disables the destructive button while the delete runs", () => {
    render(<DeleteConfirm subject="lesson" busy onConfirm={() => undefined} onCancel={() => undefined} />);

    expect(screen.getByRole("button", { name: "Delete lesson" })).toBeDisabled();
  });

  it("can use another verb in the question, the group name and the button", async () => {
    const onConfirm = vi.fn();

    render(<DeleteConfirm subject="token" verb="Revoke" onConfirm={onConfirm} onCancel={() => undefined} />);

    expect(screen.getByRole("group", { name: "Revoke this token" })).toBeInTheDocument();
    expect(screen.getByText("Revoke this token?")).toBeInTheDocument();
    expect(screen.queryByText(/Delete/)).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Revoke token" }));
    expect(onConfirm).toHaveBeenCalledOnce();
  });
});
