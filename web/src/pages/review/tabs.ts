import type { ComponentType } from "react";
import type { Role } from "../../roles";
import { ScenariosTab } from "./ScenariosTab";

/** One tab of the review queue. */
export interface ReviewTab {
  /** The value of ?tab= in the address. */
  id: string;
  label: string;
  /** The lowest role that sees the tab. */
  minRole: Role;
  Component: ComponentType;
}

/**
 * The tabs of the review queue, in the order they show. Each tab is a
 * component in its own file under pages/review; a new kind of review adds
 * one entry here.
 */
export const REVIEW_TABS: readonly ReviewTab[] = [{ id: "scenarios", label: "Scenarios", minRole: "admin", Component: ScenariosTab }];
