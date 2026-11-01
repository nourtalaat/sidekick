import { useRef, type ComponentType, type KeyboardEvent } from "react";
import { useSearchParams } from "react-router";
import "./Tabs.css";

const TAB_PARAM = "tab";

/** One tab of a page: the value of the address parameter, its label and the component of its panel. */
export interface TabDef {
  /** The value of the tab address parameter, such as "lessons" in /review?tab=lessons. */
  id: string;
  label: string;
  Panel: ComponentType;
}

interface TabsProps {
  /** The tabs in the order they are shown. The first one opens when the address names none. */
  tabs: readonly TabDef[];
  /** The accessible name of the tab list. */
  label: string;
  /** Starts the ids of the tabs and panels, so two pages never share one. */
  idPrefix: string;
}

/**
 * A tab list with the panel of the selected tab under it. The tab is kept in
 * the address (`?tab=`), so a link opens the page on it, and choosing a tab
 * drops the other parameters of the address. The tabs follow the pattern of
 * the WAI-ARIA tab list: one stop in the tab order, and the arrow keys, Home
 * and End move between them.
 */
export function Tabs({ tabs, label, idPrefix }: TabsProps) {
  const [params, setParams] = useSearchParams();
  const tabRefs = useRef<(HTMLButtonElement | null)[]>([]);

  const selected = tabs.find((t) => t.id === params.get(TAB_PARAM)) ?? tabs[0];

  if (!selected) {
    return null;
  }

  function select(index: number) {
    const tab = tabs[index];
    if (!tab) {
      return;
    }

    setParams({ [TAB_PARAM]: tab.id }, { replace: true });
    tabRefs.current[index]?.focus();
  }

  function onKeyDown(event: KeyboardEvent, index: number) {
    const last = tabs.length - 1;
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
    <>
      <div role="tablist" aria-label={label} className="sk-tabs">
        {tabs.map((tab, index) => (
          <button
            key={tab.id}
            ref={(el) => {
              tabRefs.current[index] = el;
            }}
            type="button"
            role="tab"
            id={`${idPrefix}-tab-${tab.id}`}
            aria-selected={tab.id === selected.id}
            aria-controls={`${idPrefix}-panel-${tab.id}`}
            tabIndex={tab.id === selected.id ? 0 : -1}
            className="sk-tabs__tab"
            onClick={() => select(index)}
            onKeyDown={(event) => onKeyDown(event, index)}
          >
            {tab.label}
          </button>
        ))}
      </div>
      <div role="tabpanel" id={`${idPrefix}-panel-${selected.id}`} aria-labelledby={`${idPrefix}-tab-${selected.id}`} className="sk-tabs__panel">
        <selected.Panel />
      </div>
    </>
  );
}
