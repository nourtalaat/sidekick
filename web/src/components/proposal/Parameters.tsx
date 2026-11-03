import { useId } from "react";
import type { SectionProps } from "./section";

/** The parameters whole, as the action will run with them, in a block that scrolls and takes focus. */
export function Parameters({ proposal }: SectionProps) {
  const id = useId();

  return (
    <>
      <p className="sk-proposal__label" id={id}>
        Parameters, exactly as they will run
      </p>
      <pre className="sk-proposal__params" role="region" aria-labelledby={id} tabIndex={0}>
        {proposal.paramsText}
      </pre>
    </>
  );
}
