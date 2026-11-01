import type { ServiceNotesSummary } from "../../api/notes";
import { Button } from "../../components/Button";
import { Table } from "../../components/Table";
import { formatTime } from "../rules/format";
import "./LessonsPage.css";

/** The services that have notes, each with the start of its notes and who changed them last. */
export function NotesList({ services, onOpen }: { services: readonly ServiceNotesSummary[]; onOpen(service: string): void }) {
  return (
    <Table label="Services with notes">
      <thead>
        <tr>
          <th scope="col">Service</th>
          <th scope="col">Notes</th>
          <th scope="col">Last changed</th>
        </tr>
      </thead>
      <tbody>
        {services.map((s) => (
          <tr key={s.service}>
            <th scope="row">
              <Button onClick={() => onOpen(s.service)} aria-label={`Open the notes about ${s.service}`}>
                {s.service}
              </Button>
            </th>
            <td className="sk-manage__preview">{s.preview}</td>
            <td>
              <time dateTime={s.updatedAt}>{formatTime(s.updatedAt)}</time>
              <span className="sk-table__secondary">{s.updatedByName ? `by ${s.updatedByName}` : "by an API token"}</span>
            </td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}
