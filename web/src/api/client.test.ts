import { describe, expect, it } from "vitest";
import { ApiError, CSRF_HEADER, createApi } from "./client";

function recordingFetch(status = 200, body: unknown = { kind: "user", id: "u1", name: "Ada", role: "viewer", csrfToken: "t" }) {
  const requests: Request[] = [];

  const fetch = async (request: Request) => {
    requests.push(request);

    if (status === 204) {
      return new Response(null, { status });
    }

    return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
  };

  return { requests, fetch };
}

const ORIGIN = "http://sidekick.test";

describe("the API client", () => {
  it("reads /me from the API base path", async () => {
    const { requests, fetch } = recordingFetch();
    const api = createApi({ origin: ORIGIN, fetch });

    const { data } = await api.client.GET("/me");

    expect(data?.role).toBe("viewer");
    expect(requests[0]?.url).toBe(`${ORIGIN}/api/v1/me`);
    expect(requests[0]?.headers.has(CSRF_HEADER)).toBe(false);
  });

  it("sends the CSRF token on requests that change something, not on reads", async () => {
    const { requests, fetch } = recordingFetch();
    const api = createApi({ origin: ORIGIN, fetch });

    api.setCsrfToken("token-1");
    await api.client.GET("/me");
    await api.client.POST("/jobs/{id}/retry", { params: { path: { id: 1 } } });

    expect(requests[0]?.headers.get(CSRF_HEADER)).toBeNull();
    expect(requests[1]?.method).toBe("POST");
    expect(requests[1]?.headers.get(CSRF_HEADER)).toBe("token-1");
  });

  it("tells listeners about a 401, until they stop", async () => {
    const { fetch } = recordingFetch(401, { type: "about:blank", title: "Unauthorized", status: 401 });
    const api = createApi({ origin: ORIGIN, fetch });
    let calls = 0;

    const stop = api.onUnauthorized(() => {
      calls += 1;
    });

    await api.client.GET("/me");
    expect(calls).toBe(1);

    stop();
    await api.client.GET("/me");
    expect(calls).toBe(1);
  });

  it("signs out with the CSRF token", async () => {
    const { requests, fetch } = recordingFetch(204);
    const api = createApi({ origin: ORIGIN, fetch });

    api.setCsrfToken("token-2");
    await api.signOut();

    expect(requests[0]?.method).toBe("POST");
    expect(requests[0]?.url).toBe(`${ORIGIN}/auth/logout`);
    expect(requests[0]?.headers.get(CSRF_HEADER)).toBe("token-2");
  });

  it("fails a sign-out that the server refuses, with the problem's detail", async () => {
    const { fetch } = recordingFetch(403, { title: "Forbidden", status: 403, detail: "Send the session's CSRF token." });
    const api = createApi({ origin: ORIGIN, fetch });

    await expect(api.signOut()).rejects.toThrow(new ApiError(403, "Send the session's CSRF token."));
  });
});
