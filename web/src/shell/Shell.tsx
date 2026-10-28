import { useState } from "react";
import { Outlet, useLocation } from "react-router";
import { Header } from "./Header";
import { Nav } from "./Nav";
import "./Shell.css";

/** The signed-in app: the header, the left navigation and the page. */
export function Shell() {
  const location = useLocation();
  const [menu, setMenu] = useState({ open: false, path: location.pathname });

  // The menu closes when the route changes, so a tap on a link shows the page.
  const open = menu.open && menu.path === location.pathname;

  return (
    <div className="sk-shell">
      <a className="sk-skip-link" href="#main">
        Skip to content
      </a>
      <Header menuOpen={open} onToggleMenu={() => setMenu({ open: !open, path: location.pathname })} />
      <div className="sk-shell__body">
        <Nav open={open} />
        <main id="main" className="sk-shell__main" tabIndex={-1}>
          <Outlet />
        </main>
      </div>
    </div>
  );
}
