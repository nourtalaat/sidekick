import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useApi } from "./ApiContext";
import { ApiError, detailOf } from "./client";
import type { components } from "./schema";
import { saveErrorOf } from "./rules";

export type Lesson = components["schemas"]["Lesson"];
export type LessonScope = components["schemas"]["LessonScope"];
export type LessonStatus = components["schemas"]["LessonStatus"];
export type LessonList = components["schemas"]["LessonList"];
export type LessonUpdate = components["schemas"]["LessonUpdate"];
export type UsedLesson = components["schemas"]["UsedLesson"];
export type StepLessons = components["schemas"]["StepLessons"];

/** The longest lesson text the API takes, in characters. */
export const MAX_LESSON_CHARS = 400;

/** The key of every query of lessons, so a change can read them all again. */
export const LESSONS_KEY = ["lessons"] as const;

/** The most lessons the review queue reads at a time. */
const REVIEW_PAGE_SIZE = 50;

/** The lessons with a status, newest first, with how many lessons have each status. */
export function useLessons(status: LessonStatus) {
  const api = useApi();

  return useQuery({
    queryKey: [...LESSONS_KEY, status],
    queryFn: async () => {
      const { data, error, response } = await api.client.GET("/lessons", { params: { query: { status, limit: REVIEW_PAGE_SIZE } } });
      if (data === undefined) {
        throw new ApiError(response.status, detailOf(error, response.statusText));
      }

      return data;
    },
  });
}

/** Approves a lesson that waits for review, at the version that was read. */
export function useApproveLesson() {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (lesson: Pick<Lesson, "id" | "version">) => {
      const { data, error, response } = await api.client.POST("/lessons/{id}/approve", {
        params: { path: { id: lesson.id } },
        body: { version: lesson.version },
      });
      if (data === undefined) {
        throw saveErrorOf(error, response);
      }

      return data;
    },
    onSettled: () => {
      // Not awaited: the mutation ends at once, while the card that started it is still there to hear it.
      void queryClient.invalidateQueries({ queryKey: LESSONS_KEY });
    },
  });
}

/** Replaces the text and scope of a lesson, and may reject it, at the version that was read. */
export function useUpdateLesson() {
  const api = useApi();
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async ({ id, body }: { id: string; body: LessonUpdate }) => {
      const { data, error, response } = await api.client.PUT("/lessons/{id}", { params: { path: { id } }, body });
      if (data === undefined) {
        throw saveErrorOf(error, response);
      }

      return data;
    },
    onSettled: () => {
      // Not awaited: the mutation ends at once, while the card that started it is still there to hear it.
      void queryClient.invalidateQueries({ queryKey: LESSONS_KEY });
    },
  });
}

/** The scope of a lesson in words, such as "service checkout-api, alert Latency", or "every investigation". */
export function describeScope(scope: LessonScope): string {
  const parts = [
    scope.service && `service ${scope.service}`,
    scope.alert && `alert ${scope.alert}`,
    scope.route && `route ${scope.route}`,
    scope.strategy && `strategy ${scope.strategy}`,
  ].filter(Boolean);

  return parts.length > 0 ? parts.join(", ") : "every investigation";
}
