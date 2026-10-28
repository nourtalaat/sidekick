import { useId, type SelectHTMLAttributes } from "react";
import "./TextField.css";

interface SelectFieldProps extends Omit<SelectHTMLAttributes<HTMLSelectElement>, "id"> {
  label: string;
  options: readonly { value: string; label: string }[];
}

/** A select with its label above it, styled like TextField. */
export function SelectField({ label, options, ...rest }: SelectFieldProps) {
  const id = useId();

  return (
    <div className="sk-field">
      <label htmlFor={id} className="sk-field__label">
        {label}
      </label>
      <select id={id} className="sk-field__input" {...rest}>
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
    </div>
  );
}
