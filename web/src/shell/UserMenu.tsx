import { useQueryClient } from "@tanstack/react-query";
import { ChevronDown, LogOut } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { useApi } from "../api/ApiContext";
import { Button } from "../components/Button";
import { LOGIN_ROUTE } from "../routes";
import { useCurrentUser } from "../session";

/** Shows who is signed in, with their role, and the Sign out button. */
export function UserMenu() {
  const me = useCurrentUser();
  const api = useApi();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [failed, setFailed] = useState(false);
  const container = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const panelId = useId();

  useEffect(() => {
    if (!open) {
      return;
    }

    const closeOnOutsideClick = (event: MouseEvent) => {
      if (event.target instanceof Node && !container.current?.contains(event.target)) {
        setOpen(false);
      }
    };

    document.addEventListener("mousedown", closeOnOutsideClick);

    return () => document.removeEventListener("mousedown", closeOnOutsideClick);
  }, [open]);

  async function signOut() {
    setFailed(false);

    try {
      await api.signOut();
    } catch {
      setFailed(true);

      return;
    }

    queryClient.clear();
    void navigate(LOGIN_ROUTE, { replace: true });
  }

  return (
    <div
      className="sk-user"
      ref={container}
      onKeyDown={(event) => {
        if (event.key === "Escape" && open) {
          setOpen(false);
          trigger.current?.focus();
        }
      }}
    >
      <button type="button" ref={trigger} className="sk-user__trigger" aria-expanded={open} aria-controls={panelId} onClick={() => setOpen(!open)}>
        <span className="sk-user__name">{me.name}</span>
        <ChevronDown size={16} aria-hidden="true" />
      </button>
      {open && (
        <div id={panelId} className="sk-user__panel">
          <p className="sk-user__who">{me.name}</p>
          <p className="sk-user__role">Role: {me.role}</p>
          <Button onClick={() => void signOut()}>
            <LogOut size={16} aria-hidden="true" />
            Sign out
          </Button>
          {failed && (
            <p role="alert" className="sk-user__error">
              Could not sign out. Try again.
            </p>
          )}
        </div>
      )}
    </div>
  );
}
