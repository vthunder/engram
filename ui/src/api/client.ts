import { getConfig } from "@/config";

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

export async function apiFetch(
  path: string,
  init: RequestInit = {},
): Promise<Response> {
  const { apiUrl, apiKey } = getConfig();
  const url = `${apiUrl}${path}`;

  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    ...(init.headers as Record<string, string>),
  };

  if (apiKey) {
    headers["Authorization"] = `Bearer ${apiKey}`;
  }

  const res = await fetch(url, { ...init, headers });

  if (!res.ok) {
    let code = "request_error";
    let message = res.statusText;
    try {
      const body = (await res.json()) as { error?: string; message?: string };
      code = body.error ?? code;
      message = body.message ?? message;
    } catch {
      // ignore
    }
    throw new ApiError(res.status, code, message);
  }

  return res;
}

export const api = {
  async get<T>(path: string): Promise<T> {
    const res = await apiFetch(path);
    return res.json() as Promise<T>;
  },

  async post<T>(path: string, body?: unknown): Promise<T> {
    const res = await apiFetch(path, {
      method: "POST",
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
    return res.json() as Promise<T>;
  },

  async delete(path: string): Promise<void> {
    await apiFetch(path, { method: "DELETE" });
  },
};
