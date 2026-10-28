import { useId, type ReactNode } from "react";
import "./Card.css";

/** A raised surface with a title. The title is a level 2 heading under the page title. */
export function Card({ title, children }: { title: string; children: ReactNode }) {
  const titleId = useId();

  return (
    <section className="sk-card" aria-labelledby={titleId}>
      <h2 id={titleId} className="sk-card__title">
        {title}
      </h2>
      {children}
    </section>
  );
}
