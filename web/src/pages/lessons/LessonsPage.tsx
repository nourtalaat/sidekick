import { Tabs } from "../../components/Tabs";
import { LESSONS_TABS } from "./tabs";

/**
 * The page where an admin manages what Sidekick learned: the lessons that
 * prompts show the model, and the memory of confirmed investigations. The tab
 * is kept in the address, so a link opens the page on it.
 */
export function LessonsPage() {
  return (
    <div className="sk-page">
      <h1 className="sk-page__title">Lessons and memory</h1>
      <p>What Sidekick has learned from feedback and from confirmed investigations, and what it shows the model.</p>
      <Tabs tabs={LESSONS_TABS} label="Lessons and memory" idPrefix="lessons" />
    </div>
  );
}
