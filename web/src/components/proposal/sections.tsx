import { Changes } from "./Changes";
import { Facts } from "./Facts";
import { FileDiffs } from "./FileDiffs";
import { Outcome } from "./Outcome";
import { Parameters } from "./Parameters";
import { Reason } from "./Reason";
import { Revisions } from "./Revisions";
import type { Section } from "./section";

/**
 * The sections of the card in display order. A later ticket adds a section by adding one file and one line here.
 * The places that are still free: the pull request, after Outcome.
 */
export const sections: Section[] = [Facts, Parameters, Changes, FileDiffs, Revisions, Reason, Outcome /* pull request */];
