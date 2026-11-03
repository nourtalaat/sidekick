import { Menu, Search } from "lucide-react";
import { useState, type FormEvent } from "react";
import { Link, useLocation, useNavigate } from "react-router";
import lockupDark from "../../../assets/logo/sidekick-lockup-dark.svg";
import lockupLight from "../../../assets/logo/sidekick-lockup-light.svg";
import { INVESTIGATIONS_PATH, SEARCH_MAX_LENGTH, filtersFromParams, searchPath } from "../pages/investigations/filters";
import { MAIN_NAV_ID } from "./Nav";
import { ThemeSwitch } from "./ThemeSwitch";
import { UserMenu } from "./UserMenu";

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
      <HeaderSearch />
      <ThemeSwitch />
      <UserMenu />
    </header>
  );
}

/**
 * The search box. It sends its text to the investigation list when the form is
 * sent, and shows the list's own text filter while the list is open, so the
 * two stay the same after Apply, Clear, a link or Back.
 */
function HeaderSearch() {
  const navigate = useNavigate();
  const { pathname, search } = useLocation();
  const inList = pathname === INVESTIGATIONS_PATH;
  const q = (inList && filtersFromParams(new URLSearchParams(search)).q) || "";

  // Typing stays in the box until it is sent; a new q in the address replaces it.
  const [text, setText] = useState(q);
  const [seen, setSeen] = useState(q);
  if (seen !== q) {
    setSeen(q);
    setText(q);
  }

  const submit = (event: FormEvent) => {
    event.preventDefault();
    void navigate(searchPath(text));
  };

  return (
    <form role="search" aria-label="Search investigations" className="sk-search" onSubmit={submit}>
      <Search size={16} aria-hidden="true" />
      <label htmlFor="sk-search-input" className="sk-visually-hidden">
        Search investigations
      </label>
      <input
        id="sk-search-input"
        type="search"
        name="q"
        value={text}
        maxLength={SEARCH_MAX_LENGTH}
        autoComplete="off"
        placeholder="Search investigations"
        onChange={(event) => setText(event.target.value)}
      />
    </form>
  );
}
