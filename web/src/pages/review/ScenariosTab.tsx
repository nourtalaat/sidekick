import { useSearchParams } from "react-router";
import { ApiError } from "../../api/client";
import { QueryError } from "../../components/QueryError";
import { hasRole } from "../../roles";
import { useCurrentUser } from "../../session";
import { ScenarioDetailView } from "./ScenarioDetailView";
import { ScenarioList } from "./ScenarioList";

/** The parameter of the address that names the scenario being reviewed. */
export const SCENARIO_PARAM = "id";

/**
 * The scenarios tab: the list of scenario drafts and reviewed scenarios, and,
 * when the address names one, that scenario to review. Scenarios are for
 * admins.
 */
export function ScenariosTab() {
  const me = useCurrentUser();
  const [params] = useSearchParams();
  const id = params.get(SCENARIO_PARAM);

  if (!hasRole(me.role, "admin")) {
    return <QueryError error={new ApiError(403, "This needs the admin role.")} />;
  }

  return id ? <ScenarioDetailView key={id} id={id} /> : <ScenarioList />;
}
