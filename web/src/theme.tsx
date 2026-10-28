import { createContext, useContext, useEffect, useState, type ReactNode } from "react";

export type ThemeChoice = "system" | "light" | "dark";

export const THEME_STORAGE_KEY = "sidekick-theme";

const CHOICES: readonly ThemeChoice[] = ["system", "light", "dark"];

interface ThemeValue {
  choice: ThemeChoice;
  setChoice(choice: ThemeChoice): void;
}

const ThemeContext = createContext<ThemeValue | undefined>(undefined);

/**
 * Keeps the theme choice. With "system", the page has no data-theme
 * attribute and the tokens follow prefers-color-scheme; "light" and "dark"
 * set the attribute, which wins. The choice is kept in localStorage until
 * it is saved per user on the server.
 */
export function ThemeProvider({ children }: { children: ReactNode }) {
  const [choice, setChoice] = useState<ThemeChoice>(readStoredChoice);

  useEffect(() => {
    applyTheme(choice);
    storeChoice(choice);
  }, [choice]);

  return <ThemeContext value={{ choice, setChoice }}>{children}</ThemeContext>;
}

export function useTheme(): ThemeValue {
  const theme = useContext(ThemeContext);
  if (!theme) {
    throw new Error("useTheme needs a ThemeProvider");
  }

  return theme;
}

/** Sets the theme on the page before the first render, so a stored choice does not flash. */
export function applyStoredTheme(): void {
  applyTheme(readStoredChoice());
}

function applyTheme(choice: ThemeChoice): void {
  const root = document.documentElement;

  if (choice === "system") {
    root.removeAttribute("data-theme");
  } else {
    root.setAttribute("data-theme", choice);
  }
}

function readStoredChoice(): ThemeChoice {
  try {
    const stored = localStorage.getItem(THEME_STORAGE_KEY);

    return CHOICES.find((choice) => choice === stored) ?? "system";
  } catch {
    return "system";
  }
}

function storeChoice(choice: ThemeChoice): void {
  try {
    localStorage.setItem(THEME_STORAGE_KEY, choice);
  } catch {
    // Storage is blocked; the choice lasts until the page is closed.
  }
}
