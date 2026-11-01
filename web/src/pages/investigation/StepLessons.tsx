import { describeScope, type StepLessons as StepLessonsRecord } from "../../api/lessons";

const REASONS: Record<StepLessonsRecord["leftOut"][number]["reason"], string> = {
  max_per_prompt: "the prompt already had the most lessons",
  max_tokens: "it did not fit in the tokens set aside for lessons",
  global_share: "global lessons had their share of the prompt already",
};

/** What the lessons of a prompt step were: how many it used, and the ones that applied but did not fit, with why. */
export function StepLessons({ lessons }: { lessons: StepLessonsRecord }) {
  return (
    <div className="sk-step__lessons">
      <p>
        {lessons.used.length} {lessons.used.length === 1 ? "lesson" : "lessons"} used, from {lessons.candidates} that applied.
      </p>
      {lessons.leftOutTotal > 0 && (
        <>
          <p>
            {lessons.leftOutTotal} left out
            {lessons.leftOut.length < lessons.leftOutTotal ? `; the first ${lessons.leftOut.length} are listed` : ""}. A reason that repeats can mean a scope is
            too broad.
          </p>
          <ul>
            {lessons.leftOut.map((l) => (
              <li key={l.id}>
                Lesson for {describeScope(l.scope)}: {REASONS[l.reason]}
              </li>
            ))}
          </ul>
        </>
      )}
    </div>
  );
}
