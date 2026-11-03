import type { ReactElement } from "react";
import type { Proposal } from "../../api/proposals";

/** What every section gets. */
export interface SectionProps {
  proposal: Proposal;
  /** True in the review queue, where the card names its investigation. */
  inQueue: boolean;
}

/** A section of the proposal card: a component that returns null when it has nothing to show. */
export type Section = (props: SectionProps) => ReactElement | null;
