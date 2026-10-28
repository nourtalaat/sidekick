import { CircleX } from "lucide-react";
import { useId, type ReactNode } from "react";
import "./TextField.css";

/** The attributes a control needs to belong to its field. */
export interface FieldControlProps {
  id: string;
  "aria-invalid"?: true;
  "aria-describedby"?: string;
}

interface FieldProps {
  label: string;
  /** Text under the control that says what to enter. */
  help?: string;
  /** Says what is wrong, in words. Shown with an icon in the danger color. */
  error?: string;
  /** Renders the control with the attributes that tie it to the label, help and error. */
  children: (control: FieldControlProps) => ReactNode;
}

/** A label above a control, with help and error text below it. */
export function Field({ label, help, error, children }: FieldProps) {
  const id = useId();
  const helpId = `${id}-help`;
  const errorId = `${id}-error`;
  const describedBy = [help ? helpId : undefined, error ? errorId : undefined].filter(Boolean).join(" ") || undefined;

  return (
    <div className="sk-field">
      <label htmlFor={id} className="sk-field__label">
        {label}
      </label>
      {children({ id, "aria-invalid": error ? true : undefined, "aria-describedby": describedBy })}
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
