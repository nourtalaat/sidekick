import type { TabDef } from "../../components/Tabs";
import { ConfigTab } from "./ConfigTab";
import { JobsTab } from "./JobsTab";
import { TokensTab } from "./TokensTab";

/** The tabs of the Settings page, in the order they are shown. The first one opens when the address names none. */
export const SETTINGS_TABS: readonly TabDef[] = [
  { id: "config", label: "Config", Panel: ConfigTab },
  { id: "tokens", label: "API tokens", Panel: TokensTab },
  { id: "failed-jobs", label: "Failed jobs", Panel: JobsTab },
];
