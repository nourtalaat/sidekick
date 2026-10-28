import type { TextareaHTMLAttributes } from "react";
import { Field } from "./Field";
import "./TextField.css";

interface TextAreaProps extends Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, "id"> {
  label: string;
  help?: string;
  error?: string;
  /** Shows the text in the monospace face, for patterns. */
  mono?: boolean;
}

/** A multi-line text input with its label above it, and help and error text below it. */
export function TextArea({ label, help, error, mono = false, rows = 3, ...rest }: TextAreaProps) {
  const className = mono ? "sk-field__input sk-field__input--area sk-field__input--mono" : "sk-field__input sk-field__input--area";

  return (
    <Field label={label} help={help} error={error}>
      {(control) => <textarea className={className} rows={rows} {...control} {...rest} />}
    </Field>
  );
}
