import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useRef } from "react";
import { useApi } from "./api/ApiContext";
import type { Me } from "./session";
import { readStoredChoice, useTheme, type ThemeChoice } from "./theme";

/**
 * Keeps the theme choice per user on the server. Once, when the session
 * starts, the server's choice wins over the one this browser kept; when the
 * server has none, a choice this browser kept is sent, so choices made
 * before the server kept any are not lost. After that, each choice the
 * person makes is sent. A failed save changes nothing on screen and shows
 * nothing; the next choice is sent as usual. Renders nothing.
 */
export function ThemeSync({ serverTheme }: { serverTheme: ThemeChoice | undefined }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const { choice, adoptChoice, onChoose } = useTheme();
  const started = useRef(false);

  const save = useCallback(
    async (theme: ThemeChoice) => {
      try {
        const { response } = await api.client.PUT("/me/theme", { body: { theme } });
        if (response.ok) {
          queryClient.setQueryData<Me>(["me"], (me) => (me ? { ...me, theme } : me));
        }
      } catch {
        // The server could not be reached; the screen keeps the choice.
      }
    },
    [api, queryClient],
  );

  useEffect(() => onChoose((next) => void save(next)), [onChoose, save]);

  useEffect(() => {
    if (started.current) {
      return;
    }

    started.current = true;

    if (serverTheme) {
      if (serverTheme !== choice) {
        adoptChoice(serverTheme);
      }

      return;
    }

    const stored = readStoredChoice();
    if (stored) {
      void save(stored);
    }
  }, [serverTheme, choice, adoptChoice, save]);

  return null;
}
