import { useState } from "react";
import { Layers, RefreshCw, Trash2, AlertTriangle } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { useSchemas, useInduceSchemas, useDeleteSchema } from "@/hooks/useSchemas";
import { shortId } from "@/lib/format";
import type { Schema } from "@/api/types";

function SchemaContent({ content }: { content: string }) {
  // Render semi-structured prose with section headers highlighted
  const lines = content.split("\n");
  return (
    <div className="text-sm space-y-1 font-mono whitespace-pre-wrap leading-relaxed">
      {lines.map((line, i) => {
        const trimmed = line.trim();
        // SCHEMA: header line
        if (trimmed.startsWith("SCHEMA:")) {
          return null; // skip — shown as card title
        }
        // Section headers: all-caps, no sentence punctuation
        const isHeader =
          trimmed.length > 2 &&
          trimmed === trimmed.toUpperCase() &&
          !/[.,;:?!]/.test(trimmed) &&
          /^[A-Z\s]+$/.test(trimmed);
        if (isHeader && trimmed !== "") {
          return (
            <p key={i} className="font-semibold text-foreground mt-3 first:mt-0 text-xs tracking-widest">
              {trimmed}
            </p>
          );
        }
        return (
          <p key={i} className={line === "" ? "h-1" : "text-muted-foreground"}>
            {line}
          </p>
        );
      })}
    </div>
  );
}

function SchemaCard({
  schema,
  selected,
  onSelect,
}: {
  schema: Schema;
  selected: boolean;
  onSelect: () => void;
}) {
  const { mutate: deleteSchema, isPending: deleting } = useDeleteSchema();

  return (
    <div
      className={`border rounded-lg p-4 cursor-pointer transition-colors ${
        selected
          ? "border-primary bg-primary/5"
          : "hover:border-muted-foreground/40 hover:bg-accent/30"
      }`}
      onClick={onSelect}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="flex items-center gap-2 min-w-0">
          <Layers className="h-4 w-4 text-muted-foreground shrink-0" />
          <span className="font-medium truncate">{schema.name}</span>
        </div>
        <div className="flex items-center gap-1.5 shrink-0">
          {schema.is_labile && (
            <Badge variant="outline" className="text-yellow-600 border-yellow-400 gap-1 text-xs">
              <AlertTriangle className="h-3 w-3" />
              labile
            </Badge>
          )}
          <span className="text-xs text-muted-foreground font-mono">{shortId(schema.id)}</span>
          <Button
            variant="ghost"
            size="icon"
            className="h-6 w-6 text-muted-foreground hover:text-destructive"
            onClick={(e) => {
              e.stopPropagation();
              if (confirm(`Delete schema "${schema.name}"?`)) {
                deleteSchema(schema.id);
              }
            }}
            disabled={deleting}
          >
            <Trash2 className="h-3 w-3" />
          </Button>
        </div>
      </div>

      <p className="mt-2 text-sm text-muted-foreground line-clamp-2">
        {extractPattern(schema.content)}
      </p>
    </div>
  );
}

function extractPattern(content: string): string {
  const lines = content.split("\n");
  let inPattern = false;
  const patternLines: string[] = [];
  for (const line of lines) {
    if (line.trim() === "PATTERN") {
      inPattern = true;
      continue;
    }
    if (inPattern) {
      const trimmed = line.trim();
      if (
        trimmed !== "" &&
        trimmed === trimmed.toUpperCase() &&
        !/[.,;:?!]/.test(trimmed) &&
        /^[A-Z\s]+$/.test(trimmed)
      ) {
        break;
      }
      if (trimmed) patternLines.push(trimmed);
    }
  }
  return patternLines.join(" ").slice(0, 200) || content.slice(0, 200);
}

export default function Schemas() {
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const { data: schemas, isLoading } = useSchemas();
  const { mutate: induceSchemas, isPending: inducing } = useInduceSchemas();

  const selected = schemas?.find((s) => s.id === selectedId) ?? null;
  const list = schemas ?? [];

  return (
    <div className="p-6 h-full flex flex-col gap-4">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Schemas</h1>
          <p className="text-sm text-muted-foreground mt-0.5">
            Cross-cutting pattern templates extracted from recurring engram clusters
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() => induceSchemas()}
          disabled={inducing}
          className="gap-2"
        >
          <RefreshCw className={`h-4 w-4 ${inducing ? "animate-spin" : ""}`} />
          {inducing ? "Inducing…" : "Run Induction"}
        </Button>
      </div>

      {/* Content */}
      {isLoading ? (
        <p className="text-sm text-muted-foreground">Loading schemas…</p>
      ) : list.length === 0 ? (
        <div className="flex-1 flex items-center justify-center">
          <div className="text-center space-y-2">
            <Layers className="h-12 w-12 text-muted-foreground/40 mx-auto" />
            <p className="text-muted-foreground">No schemas yet</p>
            <p className="text-sm text-muted-foreground/70">
              Schema induction runs automatically after L2+ engrams form.
              <br />
              You can also trigger it manually with "Run Induction".
            </p>
          </div>
        </div>
      ) : (
        <div className="flex gap-4 flex-1 min-h-0">
          {/* Schema list */}
          <div className="w-80 flex-shrink-0 overflow-y-auto space-y-2">
            <p className="text-xs text-muted-foreground">{list.length} schema{list.length !== 1 ? "s" : ""}</p>
            {list.map((s) => (
              <SchemaCard
                key={s.id}
                schema={s}
                selected={selectedId === s.id}
                onSelect={() => setSelectedId(selectedId === s.id ? null : s.id)}
              />
            ))}
          </div>

          {/* Detail panel */}
          {selected && (
            <div className="flex-1 border rounded-lg p-5 overflow-y-auto">
              <div className="flex items-start gap-2 mb-4">
                <Layers className="h-5 w-5 text-primary mt-0.5 shrink-0" />
                <div>
                  <h2 className="text-lg font-semibold">{selected.name}</h2>
                  <p className="text-xs text-muted-foreground">
                    Created {selected.created_at.slice(0, 10)} · Updated {selected.updated_at.slice(0, 10)}
                    {selected.instances?.length
                      ? ` · ${selected.instances.length} instance${selected.instances.length !== 1 ? "s" : ""}`
                      : ""}
                  </p>
                </div>
              </div>
              <SchemaContent content={selected.content} />
            </div>
          )}
        </div>
      )}
    </div>
  );
}
