import type { InputHTMLAttributes } from "react";
import { Field } from "./Field";
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
  return (
    <Field label={label} help={help} error={error}>
      {(control) => <input className="sk-field__input" {...control} {...rest} />}
    </Field>
  );
}
