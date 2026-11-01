import type { TabDef } from "../../components/Tabs";
import { LessonsTab } from "./LessonsTab";
import { MemoryTab } from "./MemoryTab";

/** The tabs of the Lessons and Memory page, in the order they are shown. The first one opens when the address names none. */
export const LESSONS_TABS: readonly TabDef[] = [
  { id: "lessons", label: "Lessons", Panel: LessonsTab },
  { id: "memory", label: "Memory", Panel: MemoryTab },
];
