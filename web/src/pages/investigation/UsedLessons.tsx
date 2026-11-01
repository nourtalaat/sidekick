import { describeScope, type UsedLesson } from "../../api/lessons";
import { Card } from "../../components/Card";
import "./UsedLessons.css";

const STATUS_NOTES: Record<UsedLesson["status"], string> = {
  proposed: "waits for review",
  active: "",
  disabled: "disabled since",
  rejected: "rejected since",
};

/**
 * The lessons the prompt of the investigation used, with their text and
 * status as they are now. The text came from people and the model, so it is
 * plain text. A lesson that was deleted since is not listed.
 */
export function UsedLessons({ lessons }: { lessons: readonly UsedLesson[] | undefined }) {
  return (
    <Card title="Lessons used">
      {!lessons || lessons.length === 0 ? (
        <p className="sk-used__none">No lesson was shown to the model.</p>
      ) : (
        <ul className="sk-used">
          {lessons.map((lesson) => (
            <li key={lesson.id} className="sk-used__lesson">
              <p className="sk-used__text">{lesson.text}</p>
              <p className="sk-used__scope">
                Applies to {describeScope(lesson.scope)}
                {STATUS_NOTES[lesson.status] && <span className="sk-used__status"> ({STATUS_NOTES[lesson.status]})</span>}
              </p>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
