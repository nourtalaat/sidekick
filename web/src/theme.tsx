import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";

export type ThemeChoice = "system" | "light" | "dark";

export const THEME_STORAGE_KEY = "sidekick-theme";

const CHOICES: readonly ThemeChoice[] = ["system", "light", "dark"];

type ChoiceListener = (choice: ThemeChoice) => void;

interface ThemeValue {
  choice: ThemeChoice;
  /** Applies and caches a choice the person made, and tells the listeners of onChoose. */
  setChoice(choice: ThemeChoice): void;
  /** Applies and caches a choice that came from the server. The listeners of onChoose are not told. */
  adoptChoice(choice: ThemeChoice): void;
  /** Calls listener after each choice the person makes. Returns the function that stops it. */
  onChoose(listener: ChoiceListener): () => void;
}

const ThemeContext = createContext<ThemeValue | undefined>(undefined);

/**
 * Keeps the theme choice. With "system", the page has no data-theme
 * attribute and the tokens follow prefers-color-scheme; "light" and "dark"
 * set the attribute, which wins. The choice is cached in localStorage, so the
 * page shows it before the server answers; ThemeSync keeps it per user on the
 * server.
 */
export function ThemeProvider({ children }: { children: ReactNode }) {
  const [choice, setChoice] = useState<ThemeChoice>(() => readStoredChoice() ?? "system");
  const listeners = useRef(new Set<ChoiceListener>());

  useEffect(() => {
    applyTheme(choice);
  }, [choice]);

  const adoptChoice = useCallback((next: ThemeChoice) => {
    setChoice(next);
    storeChoice(next);
  }, []);

  const choose = useCallback(
    (next: ThemeChoice) => {
      adoptChoice(next);
      listeners.current.forEach((listener) => listener(next));
    },
    [adoptChoice],
  );

  const onChoose = useCallback((listener: ChoiceListener) => {
    listeners.current.add(listener);

    return () => {
      listeners.current.delete(listener);
    };
  }, []);

  const value = useMemo(() => ({ choice, setChoice: choose, adoptChoice, onChoose }), [choice, choose, adoptChoice, onChoose]);

  return <ThemeContext value={value}>{children}</ThemeContext>;
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
  applyTheme(readStoredChoice() ?? "system");
}

/** The choice this browser has kept, or undefined when none was made here or the stored value is not a theme. */
export function readStoredChoice(): ThemeChoice | undefined {
  try {
    const stored = localStorage.getItem(THEME_STORAGE_KEY);

    return CHOICES.find((choice) => choice === stored);
  } catch {
    return undefined;
  }
}

function applyTheme(choice: ThemeChoice): void {
  const root = document.documentElement;

  if (choice === "system") {
    root.removeAttribute("data-theme");
  } else {
    root.setAttribute("data-theme", choice);
  }
}

function storeChoice(choice: ThemeChoice): void {
  try {
    localStorage.setItem(THEME_STORAGE_KEY, choice);
  } catch {
    // Storage is blocked; the choice lasts until the page is closed.
  }
}
