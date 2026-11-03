import { Navigate, Route, Routes } from "react-router";
import { LoginPage } from "./pages/LoginPage";
import { AuditPage } from "./pages/AuditPage";
import { BenchmarksPage } from "./pages/benchmarks/BenchmarksPage";
import { ComparePage } from "./pages/benchmarks/ComparePage";
import { BenchRunPage } from "./pages/benchmarks/BenchRunPage";
import { DashboardPage } from "./pages/dashboard/DashboardPage";
import InvestigationPage from "./pages/investigation/InvestigationPage";
import InvestigationsPage from "./pages/investigations/InvestigationsPage";
import { ListenerFormPage } from "./pages/ListenerFormPage";
import { ListenersPage } from "./pages/ListenersPage";
import { NotFoundPage } from "./pages/NotFoundPage";
import { RouteFormPage } from "./pages/RouteFormPage";
import { RoutesPage } from "./pages/RoutesPage";
import { SettingsPage } from "./pages/settings/SettingsPage";
import { LessonsPage } from "./pages/lessons/LessonsPage";
import { ReviewPage } from "./pages/review/ReviewPage";
import { StrategiesPage } from "./pages/StrategiesPage";
import { StrategyDiffPage } from "./pages/StrategyDiffPage";
import { StrategyPage } from "./pages/StrategyPage";
import { StrategyVersionPage } from "./pages/StrategyVersionPage";
import { LOGIN_ROUTE } from "./routes";
import { RequireRole } from "./components/RequireRole";
import { RequireSession } from "./session";
import { Shell } from "./shell/Shell";
import { UnauthorizedRedirect } from "./UnauthorizedRedirect";

/** The routes of the app. Everything but the login page needs a session. */
export function App() {
  return (
    <>
      <UnauthorizedRedirect />
      <Routes>
        <Route path={LOGIN_ROUTE} element={<LoginPage />} />
        <Route element={<RequireSession />}>
          <Route element={<Shell />}>
            <Route index element={<Navigate to="/investigations" replace />} />
            <Route path="/investigations" element={<InvestigationsPage />} />
            <Route path="/investigations/:id" element={<InvestigationPage />} />
            <Route path="/routes" element={<RoutesPage />} />
            <Route path="/routes/new" element={<RouteFormPage mode="new" />} />
            <Route path="/routes/:name" element={<RouteFormPage mode="edit" />} />
            <Route path="/listeners" element={<ListenersPage />} />
            <Route path="/listeners/new" element={<ListenerFormPage mode="new" />} />
            <Route path="/listeners/:name" element={<ListenerFormPage mode="edit" />} />
            <Route path="/strategies" element={<StrategiesPage />} />
            <Route path="/strategies/:name" element={<StrategyPage />} />
            <Route path="/strategies/:name/versions/:version" element={<StrategyVersionPage />} />
            <Route path="/strategies/:name/diff" element={<StrategyDiffPage />} />
            <Route
              path="/audit"
              element={
                <RequireRole role="admin" title="Audit log">
                  <AuditPage />
                </RequireRole>
              }
            />
            <Route path="/review" element={<ReviewPage />} />
            <Route path="/lessons" element={<LessonsPage />} />
            <Route path="/dashboard" element={<DashboardPage />} />
            <Route path="/benchmarks" element={<BenchmarksPage />} />
            <Route path="/benchmarks/compare" element={<ComparePage />} />
            <Route path="/benchmarks/:id" element={<BenchRunPage />} />
            <Route
              path="/settings"
              element={
                <RequireRole role="admin" title="Settings">
                  <SettingsPage />
                </RequireRole>
              }
            />
            <Route path="*" element={<NotFoundPage />} />
          </Route>
        </Route>
      </Routes>
    </>
  );
}
