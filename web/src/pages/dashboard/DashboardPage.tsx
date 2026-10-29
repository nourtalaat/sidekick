import { useState } from "react";
import { useSearchParams } from "react-router";
import { useCostStats, useOperationsStats, useQualityStats, useStatsFilters } from "../../api/stats";
import { EmptyState } from "../../components/EmptyState";
import { CostSection } from "./CostSection";
import { eachDay } from "./data";
import { FilterBar } from "./FilterBar";
import { OperationsSection } from "./OperationsSection";
import { filtersFromParams, paramsFromFilters, statsQueryOf, type DashboardFilters } from "./params";
import { QualitySection } from "./QualitySection";
import { StatTiles } from "./StatTiles";
import "./Dashboard.css";

/**
 * The dashboard: one row of filters for the whole page, the four key
 * numbers, and the Quality, Cost and Operations sections. The filters are
 * in the URL, so a view can be shared.
 */
export function DashboardPage() {
  const [params, setParams] = useSearchParams();
  // The presets end on the day the page opened, so the queries do not change under the viewer.
  const [now] = useState(() => new Date());

  const filters = filtersFromParams(params);
  const query = statsQueryOf(filters, now);
  const days = eachDay(query.from, query.to);

  const options = useStatsFilters();
  const quality = useQualityStats(query);
  const cost = useCostStats(query);
  const operations = useOperationsStats(query);

  const setFilters = (next: DashboardFilters) => setParams(paramsFromFilters(next), { replace: true });

  const empty =
    quality.isSuccess && cost.isSuccess && operations.isSuccess && quality.data.totals.rated === 0 && cost.data.totals.costMicros === 0 && operations.data.totals.investigations === 0;

  return (
    <div className="sk-page">
      <h1 className="sk-page__title">Dashboard</h1>
      <FilterBar filters={filters} options={options.data} now={now} onChange={setFilters} />
      <StatTiles quality={quality.data} operations={operations.data} cost={cost.data} />
      {empty ? (
        <EmptyState title="Nothing to show yet">
          No investigations, ratings or spend in this range. Change the filters, or wait for the next alert.
        </EmptyState>
      ) : (
        <>
          <QualitySection quality={quality} days={days} filters={filters} onChange={setFilters} />
          <CostSection cost={cost} days={days} />
          <OperationsSection operations={operations} days={days} />
        </>
      )}
    </div>
  );
}
