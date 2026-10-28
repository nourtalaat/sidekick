import { CircleX } from "lucide-react";
import { useId, type InputHTMLAttributes } from "react";
import "./TextField.css";

interface TextFieldProps extends Omit<InputHTMLAttributes<HTMLInputElement>, "id"> {
  label: string;
  /** Text under the field that says what to enter. */
  help?: string;
  /** Says what is wrong, in words. Shown with an icon in the danger color. */
  error?: string;
}

/** A text input with its label above it, and help and error text below it. */
export function TextField({ label, help, error, ...rest }: TextFieldProps) {
  const id = useId();
  const helpId = `${id}-help`;
  const errorId = `${id}-error`;
  const describedBy = [help ? helpId : undefined, error ? errorId : undefined].filter(Boolean).join(" ") || undefined;

  return (
    <div className="sk-field">
      <label htmlFor={id} className="sk-field__label">
        {label}
      </label>
      <input id={id} className="sk-field__input" aria-invalid={error ? true : undefined} aria-describedby={describedBy} {...rest} />
      {help && (
        <p id={helpId} className="sk-field__help">
          {help}
        </p>
      )}
      {error && (
        <p id={errorId} className="sk-field__error">
          <CircleX size={16} aria-hidden="true" />
          {error}
        </p>
      )}
    </div>
  );
}
