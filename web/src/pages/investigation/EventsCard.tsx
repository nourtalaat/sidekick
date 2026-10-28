import type { EventInfo } from "../../api/investigations";
import { formatTime } from "../../format";
import { safeHttpUrl } from "../../safeUrl";
import "./EventsCard.css";

/**
 * The alerts and messages of the group. The sender wrote their text, so it is
 * plain text; a link opens only when it is an http or https address.
 */
export function EventsCard({ events, total }: { events: readonly EventInfo[]; total: number }) {
  if (events.length === 0) {
    return null;
  }

  return (
    <details className="sk-events">
      <summary>{total === 1 ? "The alert" : `The ${total} alerts in this group`}</summary>
      <ul className="sk-events__list">
        {events.map((event) => (
          <li key={event.id}>
            <p className="sk-events__title">{event.title || event.alertName || "(no title)"}</p>
            <p className="sk-events__meta">
              {[event.sender?.name, event.severity, formatTime(event.receivedAt)].filter(Boolean).join(" · ")}
            </p>
            {event.body && <p className="sk-plain sk-events__body">{event.body}</p>}
            {event.links.length > 0 && (
              <ul className="sk-evidence">
                {event.links.map((link, i) => {
                  const url = safeHttpUrl(link.url);

                  return (
                    <li key={i}>
                      {url ? (
                        <a href={url} target="_blank" rel="noopener noreferrer">
                          {link.title}
                        </a>
                      ) : (
                        link.title
                      )}
                    </li>
                  );
                })}
              </ul>
            )}
          </li>
        ))}
      </ul>
    </details>
  );
}
