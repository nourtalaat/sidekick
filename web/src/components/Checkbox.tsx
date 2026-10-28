import { useId, type InputHTMLAttributes } from "react";
import "./Checkbox.css";

interface CheckboxProps extends Omit<InputHTMLAttributes<HTMLInputElement>, "id" | "type"> {
  label: string;
  /** Text under the box that says what it does. */
  help?: string;
}

/** A check box with its label beside it, and help text below. */
export function Checkbox({ label, help, ...rest }: CheckboxProps) {
  const id = useId();
  const helpId = `${id}-help`;

  return (
    <div className="sk-check">
      <input id={id} type="checkbox" className="sk-check__box" aria-describedby={help ? helpId : undefined} {...rest} />
      <label htmlFor={id} className="sk-check__label">
        {label}
      </label>
      {help && (
        <p id={helpId} className="sk-check__help">
          {help}
        </p>
      )}
    </div>
  );
}
