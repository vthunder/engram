export type EngramType = "knowledge" | "operational";

export type EntityType =
  | "PERSON"
  | "ORG"
  | "GPE"
  | "LOC"
  | "FAC"
  | "PRODUCT"
  | "EVENT"
  | "WORK_OF_ART"
  | "LAW"
  | "LANGUAGE"
  | "NORP"
  | "DATE"
  | "TIME"
  | "MONEY"
  | "PERCENT"
  | "QUANTITY"
  | "CARDINAL"
  | "ORDINAL"
  | "EMAIL"
  | "PET"
  | "TECHNOLOGY"
  | "OTHER";

export interface Engram {
  id: string;
  summary: string;
  level?: number;
  topic?: string;
  engram_type?: EngramType;
  activation: number;
  strength: number;
  event_time: string;
  created_at: string;
  last_accessed: string;
  labile_until?: string;
  source_ids?: string[];
  entity_ids?: string[];
}

export interface EngramCard {
  id: string;
  summary: string;
  event_time: string;
  level?: number;
}

export interface Episode {
  id: string;
  content: string;
  level?: number;
  token_count?: number;
  source?: string;
  author?: string;
  author_id?: string;
  channel?: string;
  timestamp_event: string;
  timestamp_ingested?: string;
  dialogue_act?: string;
  entropy_score?: number;
  reply_to?: string;
  created_at?: string;
}

export interface EpisodeCard {
  id: string;
  content: string;
  timestamp_event: string;
  author?: string;
  level?: number;
}

export interface Entity {
  id: string;
  name: string;
  type?: EntityType;
  salience?: number;
  aliases?: string[];
  summary?: string;
  level?: number;
  created_at?: string;
  updated_at?: string;
}

export interface EntityCard {
  id: string;
  name: string;
  level?: number;
}

export interface EngramContext {
  engram: Engram;
  source_episodes: Episode[];
  linked_entities: Entity[];
}

export interface EpisodeCountResponse {
  count: number;
}

export interface ConsolidateResponse {
  engrams_created: number;
  duration_ms: number;
}

export interface DecayResponse {
  updated: number;
}

export interface ResetResponse {
  ok: boolean;
}

export interface HealthResponse {
  status: string;
  time: string;
}

export interface ApiError {
  error: string;
  message: string;
}
