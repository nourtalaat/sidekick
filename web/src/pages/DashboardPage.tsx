import { lazy, Suspense } from "react";
import { Card } from "../components/Card";

// The chart library is the largest part of the bundle, so only this page loads it.
const VerdictChart = lazy(() => import("./VerdictChart"));

/** The dashboard placeholder, with one sample chart that shows how charts look. */
export function DashboardPage() {
  return (
    <div className="sk-page">
      <h1 className="sk-page__title">Dashboard</h1>
      <p>The dashboard comes in a later release. The chart below uses sample data.</p>
      <Card title="Investigations per day by verdict">
        <Suspense fallback={<p role="status">Loading chart</p>}>
          <VerdictChart />
        </Suspense>
      </Card>
    </div>
  );
}
