import { Tabs } from "../../components/Tabs";
import { SETTINGS_TABS } from "./tabs";

/**
 * The page where an admin sees how Sidekick is set up. Each part is a tab,
 * listed in tabs.ts. The tab is kept in the address, so a link opens the page
 * on it.
 */
export function SettingsPage() {
  return (
    <div className="sk-page">
      <h1 className="sk-page__title">Settings</h1>
      <p>How Sidekick is set up. Everything here is for admins.</p>
      <Tabs tabs={SETTINGS_TABS} label="Settings" idPrefix="settings" />
    </div>
  );
}
