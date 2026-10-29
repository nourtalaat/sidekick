import { useId, type ReactNode } from "react";
import "./Card.css";

/**
 * A raised surface with a title. The title is a level 2 heading under the
 * page title, or a level 3 heading when the card sits in a section that has
 * a level 2 heading of its own.
 */
export function Card({ title, level = 2, children }: { title: string; level?: 2 | 3; children: ReactNode }) {
  const titleId = useId();
  const Heading = level === 2 ? "h2" : "h3";

  return (
    <section className="sk-card" aria-labelledby={titleId}>
      <Heading id={titleId} className="sk-card__title">
        {title}
      </Heading>
      {children}
    </section>
  );
}
