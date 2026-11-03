import { useEffect } from "react";
import { useLocation } from "react-router";
import type { Proposal } from "../../api/proposals";
import { proposalAnchor } from "../../components/proposal/anchor";

/**
 * Scrolls to the card that the address names with #proposal-<id> and moves
 * focus to it, once the proposals are on the page. The router does not do it
 * by itself. It runs again when the same link is followed again, and not when
 * the proposals are read again.
 */
export function useFocusProposal(proposals: readonly Proposal[]): void {
  const { hash, key } = useLocation();
  const target = hash.slice(1);
  const found = proposals.some((p) => proposalAnchor(p.id) === target);

  useEffect(() => {
    if (!found) {
      return;
    }

    const card = document.getElementById(target);
    card?.scrollIntoView({ block: "start" });
    card?.focus({ preventScroll: true });
  }, [target, found, key]);
}
