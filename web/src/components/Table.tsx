import type { ReactNode } from "react";
import "./Table.css";

/**
 * A table in a region that scrolls sideways on a narrow screen and that the
 * keyboard can focus, so it can be scrolled without a mouse. Give it a
 * label that names what the rows are. className adds a modifier to the table.
 */
export function Table({ label, className, children }: { label: string; className?: string; children: ReactNode }) {
  return (
    <div className="sk-table-wrap" role="region" aria-label={label} tabIndex={0}>
      <table className={className ? `sk-table ${className}` : "sk-table"}>{children}</table>
    </div>
  );
}
