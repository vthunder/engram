import { api } from "./client";
import type {
  ConsolidateResponse,
  DecayResponse,
  ResetResponse,
  HealthResponse,
} from "./types";

export function consolidate(): Promise<ConsolidateResponse> {
  return api.post("/v1/consolidate");
}

export function decayActivation(): Promise<DecayResponse> {
  return api.post("/v1/activation/decay");
}

export async function resetMemory(): Promise<ResetResponse> {
  await api.delete("/v1/memory/reset");
  return { ok: true };
}

export function getHealth(): Promise<HealthResponse> {
  return api.get("/health");
}
