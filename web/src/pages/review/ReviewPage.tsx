import { Link, useSearchParams } from "react-router";
import { EmptyState } from "../../components/EmptyState";
import { hasRole } from "../../roles";
import { useCurrentUser } from "../../session";
import { REVIEW_TABS } from "./tabs";
import "./ReviewPage.css";

/** The tab parameter of the address. */
export const TAB_PARAM = "tab";

/**
 * The review queue: one page with one tab for each kind of thing that waits
 * for a person. The tab is in the address (?tab=scenarios), so a link opens
 * the right one, and a tab shows only to roles that may use it.
 */
export function ReviewPage() {
  const me = useCurrentUser();
  const [params] = useSearchParams();

  const tabs = REVIEW_TABS.filter((tab) => hasRole(me.role, tab.minRole));

  if (tabs.length === 0) {
    return (
      <div className="sk-page">
        <h1 className="sk-page__title">Review queue</h1>
        <EmptyState title="Nothing for your role to review">Drafts that need a person to check them show here for the roles that may review them.</EmptyState>
      </div>
    );
  }

  const current = tabs.find((tab) => tab.id === params.get(TAB_PARAM)) ?? tabs[0];
  const Current = current?.Component;

  return (
    <div className="sk-page">
      <h1 className="sk-page__title">Review queue</h1>
      <nav aria-label="Kinds of review" className="sk-review-tabs">
        <ul className="sk-review-tabs__list">
          {tabs.map((tab) => (
            <li key={tab.id}>
              <Link
                to={`/review?${TAB_PARAM}=${tab.id}`}
                className={tab.id === current?.id ? "sk-review-tabs__link sk-review-tabs__link--current" : "sk-review-tabs__link"}
                aria-current={tab.id === current?.id ? "page" : undefined}
              >
                {tab.label}
              </Link>
            </li>
          ))}
        </ul>
      </nav>
      {Current && <Current />}
    </div>
  );
}
