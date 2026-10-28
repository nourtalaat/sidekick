import { Navigate, Route, Routes } from "react-router";
import { LoginPage } from "./pages/LoginPage";
import { AuditPage } from "./pages/AuditPage";
import { DashboardPage } from "./pages/DashboardPage";
import { ListenerFormPage } from "./pages/ListenerFormPage";
import { ListenersPage } from "./pages/ListenersPage";
import { NotFoundPage } from "./pages/NotFoundPage";
import { PlaceholderPage } from "./pages/PlaceholderPage";
import { RouteFormPage } from "./pages/RouteFormPage";
import { RoutesPage } from "./pages/RoutesPage";
import { LOGIN_ROUTE } from "./routes";
import { RequireSession } from "./session";
import { NAV_ITEMS } from "./shell/Nav";
import { Shell } from "./shell/Shell";
import { UnauthorizedRedirect } from "./UnauthorizedRedirect";

/** The paths of the navigation that have their own routes below, so no placeholder is made for them. */
const BUILT_PATHS = ["/routes", "/listeners", "/audit"];

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
            <Route path="/routes" element={<RoutesPage />} />
            <Route path="/routes/new" element={<RouteFormPage mode="new" />} />
            <Route path="/routes/:name" element={<RouteFormPage mode="edit" />} />
            <Route path="/listeners" element={<ListenersPage />} />
            <Route path="/listeners/new" element={<ListenerFormPage mode="new" />} />
            <Route path="/listeners/:name" element={<ListenerFormPage mode="edit" />} />
            <Route path="/audit" element={<AuditPage />} />
            {NAV_ITEMS.filter(({ to }) => !BUILT_PATHS.includes(to)).map(({ to, label }) => (
              <Route key={to} path={to} element={to === "/dashboard" ? <DashboardPage /> : <PlaceholderPage title={label} />} />
            ))}
            <Route path="*" element={<NotFoundPage />} />
          </Route>
        </Route>
      </Routes>
    </>
  );
}
