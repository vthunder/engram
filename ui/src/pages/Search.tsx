import { useState, useEffect } from "react";
import { useSearchParams, useNavigate } from "react-router";
import { SearchIcon, Brain, FileText, Users } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { useSearch } from "@/hooks/useSearch";
import { relativeTime, truncate, shortId } from "@/lib/format";
import type { EntityType } from "@/api/types";
import { entityTypeColor } from "@/pages/Entities";

export default function Search() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const [input, setInput] = useState(params.get("q") ?? "");
  const query = params.get("q") ?? "";

  const { data, isLoading } = useSearch(query);

  useEffect(() => {
    setInput(params.get("q") ?? "");
  }, [params]);

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (input.trim()) {
      void navigate(`/search?q=${encodeURIComponent(input.trim())}`);
    }
  };

  return (
    <div className="p-6 space-y-6">
      <h1 className="text-2xl font-semibold">Search</h1>

      <form onSubmit={handleSubmit} className="flex gap-2 max-w-2xl">
        <Input
          value={input}
          onChange={(e) => setInput(e.target.value)}
          placeholder="Search engrams, episodes, and entities…"
          className="text-base"
          autoFocus
        />
        <Button type="submit">
          <SearchIcon className="h-4 w-4 mr-2" /> Search
        </Button>
      </form>

      {isLoading && (
        <p className="text-muted-foreground text-sm">Searching…</p>
      )}

      {data && (
        <div className="space-y-8">
          {/* Engrams */}
          <section>
            <h2 className="flex items-center gap-2 text-base font-semibold mb-3">
              <Brain className="h-4 w-4" /> Engrams
              <span className="text-muted-foreground font-normal text-sm">
                ({data.engrams.length})
              </span>
            </h2>
            {data.engrams.length === 0 ? (
              <p className="text-muted-foreground text-sm">No engrams found.</p>
            ) : (
              <div className="space-y-2">
                {data.engrams.map((e) => (
                  <div
                    key={e.id}
                    className="border rounded-md p-3 text-sm space-y-1"
                  >
                    <div className="flex items-start justify-between gap-2">
                      <p className="line-clamp-2">{e.summary}</p>
                      <Badge
                        variant={
                          e.engram_type === "operational"
                            ? "secondary"
                            : "default"
                        }
                        className="shrink-0 text-xs"
                      >
                        {e.engram_type ?? "knowledge"}
                      </Badge>
                    </div>
                    <div className="text-muted-foreground text-xs">
                      {shortId(e.id)} · activation {e.activation.toFixed(2)} ·{" "}
                      {relativeTime(e.event_time)}
                    </div>
                  </div>
                ))}
              </div>
            )}
          </section>

          {/* Episodes */}
          <section>
            <h2 className="flex items-center gap-2 text-base font-semibold mb-3">
              <FileText className="h-4 w-4" /> Episodes
              <span className="text-muted-foreground font-normal text-sm">
                ({data.episodes.length})
              </span>
            </h2>
            {data.episodes.length === 0 ? (
              <p className="text-muted-foreground text-sm">No episodes found.</p>
            ) : (
              <div className="space-y-2">
                {data.episodes.map((ep) => (
                  <div
                    key={ep.id}
                    className="border rounded-md p-3 text-sm space-y-1"
                  >
                    <p>{truncate(ep.content, 160)}</p>
                    <div className="text-muted-foreground text-xs">
                      {ep.author && <span>{ep.author} · </span>}
                      {ep.channel && <span>{ep.channel} · </span>}
                      {relativeTime(ep.timestamp_event)}
                    </div>
                  </div>
                ))}
              </div>
            )}
          </section>

          {/* Entities */}
          <section>
            <h2 className="flex items-center gap-2 text-base font-semibold mb-3">
              <Users className="h-4 w-4" /> Entities
              <span className="text-muted-foreground font-normal text-sm">
                ({data.entities.length})
              </span>
            </h2>
            {data.entities.length === 0 ? (
              <p className="text-muted-foreground text-sm">No entities found.</p>
            ) : (
              <div className="flex flex-wrap gap-2">
                {data.entities.map((ent) => (
                  <span
                    key={ent.id}
                    className={`inline-flex items-center gap-1 rounded-full px-3 py-1 text-xs font-medium ${entityTypeColor(ent.type as EntityType)}`}
                  >
                    {ent.name}
                    {ent.type && (
                      <span className="opacity-60">· {ent.type}</span>
                    )}
                  </span>
                ))}
              </div>
            )}
          </section>
        </div>
      )}

      {!isLoading && !data && query && (
        <p className="text-muted-foreground text-sm">No results.</p>
      )}
    </div>
  );
}
