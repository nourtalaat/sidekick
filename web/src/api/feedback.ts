import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useApi } from "./ApiContext";
import { ApiError, detailOf } from "./client";
import { keys, type Verdict } from "./investigations";
import type { components } from "./schema";

export type FeedbackPart = components["schemas"]["FeedbackPart"];
export type FeedbackOpinion = components["schemas"]["FeedbackOpinion"];
export type FeedbackTargetKind = components["schemas"]["FeedbackTargetKind"];
export type FeedbackRating = components["schemas"]["FeedbackRating"];
export type NewFeedback = components["schemas"]["NewFeedback"];

/** The longest reason or real cause the API takes, in characters. */
export const MAX_FEEDBACK_CHARS = 2000;

const FEEDBACK_REFETCH_MS = 30_000;

/** The target of feedback: what kind of part it is, and the ref the API names it by. */
export interface FeedbackTarget {
  kind: FeedbackTargetKind;
  ref: string;
}

/** The target of the verdict of run. */
export function verdictTarget(run: number): FeedbackTarget {
  return { kind: "verdict", ref: `run:${run}/verdict` };
}

/** The target of the finding or fix with id in the report of run. */
export function reportTarget(kind: "finding" | "fix", run: number, id: string): FeedbackTarget {
  return { kind, ref: `run:${run}/${kind}:${id}` };
}

/** The target of the tool call with callId. */
export function toolCallTarget(callId: string): FeedbackTarget {
  return { kind: "tool_call", ref: callId };
}

/** The key that finds a part among the parts the API lists. */
export function partKey(target: FeedbackTarget): string {
  return `${target.kind} ${target.ref}`;
}

/**
 * How people rated the parts of the current run, and what the signed-in user
 * said. It is read again every 30 seconds, and when the stream says the
 * investigation changed. A failure leaves the counts out; it is not worth an
 * error on the page.
 */
export function useFeedback(id: string) {
  const api = useApi();

  return useQuery({
    queryKey: keys.feedback(id),
    queryFn: async () => {
      const result = await api.client.GET("/investigations/{id}/feedback", { params: { path: { id } } });
      if (result.data === undefined) {
        throw new ApiError(result.response.status, detailOf(result.error, result.response.statusText));
      }

      return new Map(result.data.parts.map((part) => [partKey({ kind: part.targetKind, ref: part.targetRef }), part]));
    },
    refetchInterval: FEEDBACK_REFETCH_MS,
  });
}

/**
 * Sends feedback on a part. The counts and the investigation are read again
 * afterwards, since a real verdict confirms the investigation, and also after
 * a refusal, since a part the API does not know any more means the report
 * changed.
 */
export function useSubmitFeedback(id: string) {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (body: NewFeedback) => {
      const result = await api.client.POST("/investigations/{id}/feedback", { params: { path: { id } }, body });
      if (result.data === undefined) {
        throw new ApiError(result.response.status, detailOf(result.error, result.response.statusText));
      }

      return result.data;
    },
    onSettled: () =>
      Promise.all([queryClient.invalidateQueries({ queryKey: keys.feedback(id) }), queryClient.invalidateQueries({ queryKey: keys.investigation(id) })]),
  });
}

/** The verdicts a person can name as the real one, with the words the page uses for them. */
export const VERDICT_CHOICES: readonly { value: Verdict; label: string }[] = [
  { value: "true_positive", label: "Real issue" },
  { value: "false_positive", label: "False alarm" },
  { value: "inconclusive", label: "Not sure yet" },
  { value: "duplicate", label: "Duplicate" },
  { value: "known_issue", label: "Known issue" },
  { value: "flapping", label: "Flapping" },
];
