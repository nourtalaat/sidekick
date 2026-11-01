import { useSearchParams } from "react-router";
import { ScenarioDetailView } from "./ScenarioDetailView";
import { ScenarioList } from "./ScenarioList";

/** The parameter of the address that names the scenario being reviewed. */
export const SCENARIO_PARAM = "id";

/**
 * The scenarios tab: the list of scenario drafts and reviewed scenarios, and,
 * when the address names one, that scenario to review.
 */
export function ScenariosTab() {
  const [params] = useSearchParams();
  const id = params.get(SCENARIO_PARAM);

  return id ? <ScenarioDetailView key={id} id={id} /> : <ScenarioList />;
}
