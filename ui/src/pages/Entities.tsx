import { useState } from "react";
import { Badge } from "@/components/ui/badge";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useEntities, useEntityEngrams } from "@/hooks/useEntities";
import { relativeTime } from "@/lib/format";
import type { EntityType } from "@/api/types";

const ENTITY_TYPES: EntityType[] = [
  "PERSON", "ORG", "GPE", "LOC", "FAC", "PRODUCT", "EVENT",
  "WORK_OF_ART", "LAW", "LANGUAGE", "NORP", "DATE", "TIME",
  "MONEY", "PERCENT", "QUANTITY", "CARDINAL", "ORDINAL",
  "EMAIL", "PET", "TECHNOLOGY", "OTHER",
];

export function entityTypeColor(type?: EntityType): string {
  switch (type) {
    case "PERSON": return "bg-violet-100 text-violet-800";
    case "ORG": return "bg-emerald-100 text-emerald-800";
    case "GPE":
    case "LOC":
    case "FAC": return "bg-sky-100 text-sky-800";
    case "TECHNOLOGY": return "bg-orange-100 text-orange-800";
    case "EVENT": return "bg-rose-100 text-rose-800";
    case "DATE":
    case "TIME": return "bg-yellow-100 text-yellow-800";
    default: return "bg-gray-100 text-gray-700";
  }
}

function EntityDetail({ entityId }: { entityId: string }) {
  const { data: engrams, isLoading } = useEntityEngrams(entityId);

  if (isLoading) return <p className="text-sm text-muted-foreground">Loading…</p>;
  if (!engrams?.length) return <p className="text-sm text-muted-foreground">No linked engrams.</p>;

  return (
    <div className="space-y-2">
      {(engrams as Array<{ id: string; summary: string; activation: number; event_time: string }>).map((e) => (
        <div key={e.id} className="border rounded-md p-3 text-sm space-y-0.5">
          <p className="line-clamp-3">{e.summary}</p>
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <span>activation {e.activation?.toFixed(2) ?? "—"}</span>
            <span>·</span>
            <span>{relativeTime(e.event_time)}</span>
          </div>
        </div>
      ))}
    </div>
  );
}

export default function Entities() {
  const [typeFilter, setTypeFilter] = useState<EntityType | "ALL">("ALL");
  const [selectedId, setSelectedId] = useState<string | null>(null);

  const { data: entities, isLoading } = useEntities({
    type: typeFilter === "ALL" ? undefined : typeFilter,
    limit: 200,
  });

  const ents = (entities ?? []) as Array<{
    id: string;
    name: string;
    type?: EntityType;
    salience?: number;
  }>;

  return (
    <div className="p-6 space-y-4 h-full flex flex-col">
      <div className="flex items-center gap-3">
        <h1 className="text-2xl font-semibold">Entities</h1>
        <Select
          value={typeFilter}
          onValueChange={(v) => setTypeFilter(v as EntityType | "ALL")}
        >
          <SelectTrigger className="w-40">
            <SelectValue placeholder="All types" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="ALL">All types</SelectItem>
            {ENTITY_TYPES.map((t) => (
              <SelectItem key={t} value={t}>{t}</SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      <div className="flex flex-1 gap-4 overflow-hidden min-h-0">
        {/* Entity list */}
        <div className="w-72 flex-shrink-0 overflow-y-auto border rounded-md">
          {isLoading ? (
            <p className="text-sm text-muted-foreground p-4">Loading…</p>
          ) : ents.length === 0 ? (
            <p className="text-sm text-muted-foreground p-4">No entities found.</p>
          ) : (
            <div className="divide-y">
              {ents.map((ent) => (
                <button
                  key={ent.id}
                  onClick={() => setSelectedId(ent.id)}
                  className={`w-full text-left px-3 py-2.5 hover:bg-muted/50 transition-colors ${
                    selectedId === ent.id ? "bg-muted" : ""
                  }`}
                >
                  <div className="flex items-center gap-2">
                    <span className="text-sm font-medium truncate flex-1">
                      {ent.name}
                    </span>
                    <span
                      className={`inline-flex shrink-0 rounded-full px-2 py-0.5 text-xs ${entityTypeColor(ent.type)}`}
                    >
                      {ent.type ?? "?"}
                    </span>
                  </div>
                </button>
              ))}
            </div>
          )}
        </div>

        {/* Detail panel */}
        <div className="flex-1 overflow-y-auto">
          {selectedId ? (
            <div className="space-y-3">
              <h2 className="text-base font-semibold">
                Linked Engrams
                <Badge variant="secondary" className="ml-2 text-xs">
                  {ents.find((e) => e.id === selectedId)?.name}
                </Badge>
              </h2>
              <EntityDetail entityId={selectedId} />
            </div>
          ) : (
            <div className="flex items-center justify-center h-full text-muted-foreground text-sm">
              Select an entity to see linked engrams
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
