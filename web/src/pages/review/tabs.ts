import type { ComponentType } from "react";
import { LessonsTab } from "./LessonsTab";
import { ScenariosTab } from "./ScenariosTab";
import { StrategiesTab } from "./StrategiesTab";

/** One kind of thing that waits for review. Its panel is a component of its own, in this folder. */
export interface ReviewTab {
  /** The value of the tab address parameter, such as "lessons" in /review?tab=lessons. */
  id: string;
  label: string;
  Panel: ComponentType;
}

/** The tabs of the review queue, in the order they are shown. The first one opens when the address names none. */
export const REVIEW_TABS: readonly ReviewTab[] = [
  { id: "lessons", label: "Lessons", Panel: LessonsTab },
  { id: "strategies", label: "Strategies", Panel: StrategiesTab },
  { id: "scenarios", label: "Scenarios", Panel: ScenariosTab },
];
