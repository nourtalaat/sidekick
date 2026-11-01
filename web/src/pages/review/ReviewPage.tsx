import type { ComponentType } from "react";
import { Link, useSearchParams } from "react-router";
import { ActionsTab } from "./ActionsTab";
import "./review.css";

/**
 * One tab of the review queue. Each kind of thing a person reviews is a tab
 * with its own component file in this folder: add an entry here to add a
 * tab. The id is the value of `?tab=` in the address.
 */
interface Tab {
  id: string;
  label: string;
  Component: ComponentType;
}

const TABS: readonly Tab[] = [{ id: "actions", label: "Actions", Component: ActionsTab }];

/** The review queue: one page, with a tab for each kind of thing that waits for a person. */
export function ReviewPage() {
  const [params] = useSearchParams();
  const active = TABS.find((tab) => tab.id === params.get("tab")) ?? TABS[0];

  if (!active) {
    return null;
  }

  return (
    <div className="sk-page">
      <h1 className="sk-page__title">Review queue</h1>
      <nav aria-label="Kinds of review">
        <ul className="sk-review__tabs">
          {TABS.map((tab) => (
            <li key={tab.id}>
              <Link to={`/review?tab=${tab.id}`} className="sk-review__tab" aria-current={tab.id === active.id ? "page" : undefined}>
                {tab.label}
              </Link>
            </li>
          ))}
        </ul>
      </nav>
      <active.Component />
    </div>
  );
}
