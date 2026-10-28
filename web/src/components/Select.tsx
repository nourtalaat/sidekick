import type { SelectHTMLAttributes } from "react";
import { Field } from "./Field";
import "./TextField.css";

export interface SelectOption {
  value: string;
  label: string;
}

interface SelectProps extends Omit<SelectHTMLAttributes<HTMLSelectElement>, "id" | "children"> {
  label: string;
  help?: string;
  error?: string;
  options: readonly SelectOption[];
}

/** A drop-down list with its label above it, and help and error text below it. */
export function Select({ label, help, error, options, ...rest }: SelectProps) {
  return (
    <Field label={label} help={help} error={error}>
      {(control) => (
        <select className="sk-field__input" {...control} {...rest}>
          {options.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
      )}
    </Field>
  );
}
