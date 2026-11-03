import type { ReactNode } from "react";

/** One name and value of a list of facts. Put it inside a `dl` with the class sk-benchrun__facts. */
export function Fact({ name, children }: { name: string; children: ReactNode }) {
  return (
    <div className="sk-benchrun__fact">
      <dt>{name}</dt>
      <dd>{children}</dd>
    </div>
  );
}
