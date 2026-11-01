import { useRef, type KeyboardEvent } from "react";
import { useSearchParams } from "react-router";
import { REVIEW_TABS } from "./tabs";
import "./ReviewPage.css";

const TAB_PARAM = "tab";

/**
 * The review queue: one tab for each kind of thing that waits for a person.
 * The tab is kept in the address, so a link opens the queue on it. The tabs
 * follow the pattern of the WAI-ARIA tab list: one stop in the tab order, the
 * arrow keys, Home and End move between them.
 */
export function ReviewPage() {
  const [params, setParams] = useSearchParams();
  const tabRefs = useRef<(HTMLButtonElement | null)[]>([]);

  const selected = REVIEW_TABS.find((t) => t.id === params.get(TAB_PARAM)) ?? REVIEW_TABS[0];

  if (!selected) {
    return null;
  }

  function select(index: number) {
    const tab = REVIEW_TABS[index];
    if (!tab) {
      return;
    }

    setParams({ [TAB_PARAM]: tab.id }, { replace: true });
    tabRefs.current[index]?.focus();
  }

  function onKeyDown(event: KeyboardEvent, index: number) {
    const last = REVIEW_TABS.length - 1;
    const next: Record<string, number> = {
      ArrowRight: index === last ? 0 : index + 1,
      ArrowLeft: index === 0 ? last : index - 1,
      Home: 0,
      End: last,
    };

    const target = next[event.key];
    if (target !== undefined) {
      event.preventDefault();
      select(target);
    }
  }

  return (
    <div className="sk-page">
      <h1 className="sk-page__title">Review queue</h1>
      <p>What Sidekick drafted or proposed waits here until a person decides.</p>
      <div role="tablist" aria-label="Kinds of review" className="sk-review__tabs">
        {REVIEW_TABS.map((tab, index) => (
          <button
            key={tab.id}
            ref={(el) => {
              tabRefs.current[index] = el;
            }}
            type="button"
            role="tab"
            id={`review-tab-${tab.id}`}
            aria-selected={tab.id === selected.id}
            aria-controls={`review-panel-${tab.id}`}
            tabIndex={tab.id === selected.id ? 0 : -1}
            className="sk-review__tab"
            onClick={() => select(index)}
            onKeyDown={(event) => onKeyDown(event, index)}
          >
            {tab.label}
          </button>
        ))}
      </div>
      <div role="tabpanel" id={`review-panel-${selected.id}`} aria-labelledby={`review-tab-${selected.id}`} className="sk-review__panel">
        <selected.Panel />
      </div>
    </div>
  );
}
