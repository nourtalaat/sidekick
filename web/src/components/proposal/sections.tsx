import { Changes } from "./Changes";
import { Facts } from "./Facts";
import { Outcome } from "./Outcome";
import { Parameters } from "./Parameters";
import { PullRequest } from "./PullRequest";
import { Reason } from "./Reason";
import type { Section } from "./section";

/**
 * The sections of the card in display order. A later ticket adds a section by adding one file and one line here.
 * The places that are still free: files (the line diff) and revisions, between Changes and Reason.
 */
export const sections: Section[] = [Facts, Parameters, Changes, /* files, revisions */ Reason, Outcome, PullRequest];
