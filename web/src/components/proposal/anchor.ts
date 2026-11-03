/** The id of the card of the proposal with this ID, which is also the fragment of a link to it. */
export function proposalAnchor(id: string): string {
  return `proposal-${id}`;
}
