import { Tabs } from "../../components/Tabs";
import { REVIEW_TABS } from "./tabs";

/**
 * The review queue: one tab for each kind of thing that waits for a person.
 * The tabs are the shared tab list of the app, with the tab kept in the
 * address, so a link opens the queue on it.
 */
export function ReviewPage() {
  return (
    <div className="sk-page">
      <h1 className="sk-page__title">Review queue</h1>
      <p>What Sidekick drafted or proposed waits here until a person decides.</p>
      <Tabs tabs={REVIEW_TABS} label="Kinds of review" idPrefix="review" />
    </div>
  );
}
