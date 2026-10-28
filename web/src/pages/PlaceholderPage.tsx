import { EmptyState } from "../components/EmptyState";

/** A page that is not built yet: its title and the empty state. */
export function PlaceholderPage({ title }: { title: string }) {
  return (
    <div className="sk-page">
      <h1 className="sk-page__title">{title}</h1>
      <EmptyState title="Nothing here yet">Comes in a later release.</EmptyState>
    </div>
  );
}
