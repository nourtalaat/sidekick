import { Link } from "react-router";

export function NotFoundPage() {
  return (
    <div className="sk-page">
      <h1 className="sk-page__title">Page not found</h1>
      <p>
        There is no page at this address. Go to <Link to="/investigations">Investigations</Link>.
      </p>
    </div>
  );
}
