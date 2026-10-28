import { Monitor, Moon, Sun, type LucideIcon } from "lucide-react";
import { useTheme, type ThemeChoice } from "../theme";

const OPTIONS: readonly { value: ThemeChoice; label: string; Icon: LucideIcon }[] = [
  { value: "system", label: "System", Icon: Monitor },
  { value: "light", label: "Light", Icon: Sun },
  { value: "dark", label: "Dark", Icon: Moon },
];

/** Chooses System, Light or Dark. System follows the operating system's setting. */
export function ThemeSwitch() {
  const { choice, setChoice } = useTheme();

  return (
    <fieldset className="sk-theme">
      <legend className="sk-visually-hidden">Theme</legend>
      {OPTIONS.map(({ value, label, Icon }) => (
        <label key={value} className="sk-theme__option">
          <input type="radio" name="theme" value={value} checked={choice === value} onChange={() => setChoice(value)} />
          <Icon size={16} aria-hidden="true" />
          <span className="sk-theme__text">{label}</span>
        </label>
      ))}
    </fieldset>
  );
}
