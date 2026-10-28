import { Navigate, Route, Routes } from "react-router";
import { LoginPage } from "./pages/LoginPage";
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
            {NAV_ITEMS.map(({ to, label }) => (
              <Route key={to} path={to} element={<PlaceholderPage title={label} />} />
            ))}
            <Route path="*" element={<NotFoundPage />} />
          </Route>
        </Route>
      </Routes>
    </>
  );
}
