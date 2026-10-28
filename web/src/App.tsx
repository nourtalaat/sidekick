import { Navigate, Route, Routes } from "react-router";
import { LoginPage } from "./pages/LoginPage";
import { DashboardPage } from "./pages/DashboardPage";
import InvestigationsPage from "./pages/investigations/InvestigationsPage";
import { NotFoundPage } from "./pages/NotFoundPage";
import { PlaceholderPage } from "./pages/PlaceholderPage";
import { LOGIN_ROUTE } from "./routes";
import { RequireSession } from "./session";
import { NAV_ITEMS } from "./shell/Nav";
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
            {NAV_ITEMS.filter(({ to }) => to !== "/investigations").map(({ to, label }) => (
              <Route key={to} path={to} element={to === "/dashboard" ? <DashboardPage /> : <PlaceholderPage title={label} />} />
            ))}
            <Route path="*" element={<NotFoundPage />} />
          </Route>
        </Route>
      </Routes>
    </>
  );
}
