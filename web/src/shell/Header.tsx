import { Menu, Search } from "lucide-react";
import { Link } from "react-router";
import lockupDark from "../../../assets/logo/sidekick-lockup-dark.svg";
import lockupLight from "../../../assets/logo/sidekick-lockup-light.svg";
import { MAIN_NAV_ID } from "./Nav";
import { ThemeSwitch } from "./ThemeSwitch";
import { UserMenu } from "./UserMenu";

const SEARCH_NOTE = "Search comes with the investigation list.";

/** The top bar, with the brand gradient strip on its upper edge. */
export function Header({ menuOpen, onToggleMenu }: { menuOpen: boolean; onToggleMenu: () => void }) {
  return (
    <header className="sk-header">
      <button type="button" className="sk-header__menu" aria-expanded={menuOpen} aria-controls={MAIN_NAV_ID} onClick={onToggleMenu}>
        <Menu size={20} aria-hidden="true" />
        Menu
      </button>
      <Link to="/" className="sk-header__brand" aria-label="Sidekick home">
        <img className="sk-lockup sk-lockup--light" src={lockupLight} alt="" height={28} />
        <img className="sk-lockup sk-lockup--dark" src={lockupDark} alt="" height={28} />
      </Link>
      <div className="sk-search" title={SEARCH_NOTE}>
        <label htmlFor="sk-search-input" className="sk-visually-hidden">
          Search investigations
        </label>
        <Search size={16} aria-hidden="true" />
        <input id="sk-search-input" type="search" disabled placeholder="Search investigations" aria-describedby="sk-search-note" />
        <span id="sk-search-note" className="sk-visually-hidden">
          {SEARCH_NOTE}
        </span>
      </div>
      <ThemeSwitch />
      <UserMenu />
    </header>
  );
}
