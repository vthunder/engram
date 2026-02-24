import { useState } from "react";
import { Network, Trash2 } from "lucide-react";
import { useNavigate } from "react-router";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useEngrams, useEngramContext, useDeleteEngram } from "@/hooks/useEngrams";
import { relativeTime, shortId } from "@/lib/format";
import type { Engram } from "@/api/types";

function ActivationBar({ value }: { value: number }) {
  return (
    <div className="flex items-center gap-2">
      <div className="h-1.5 bg-muted rounded-full flex-1 max-w-[80px]">
        <div
          className="h-1.5 bg-primary rounded-full"
          style={{ width: `${Math.min(100, value * 100)}%` }}
        />
      </div>
      <span className="text-xs text-muted-foreground font-mono">
        {value.toFixed(2)}
      </span>
    </div>
  );
}

function EngramContextPanel({ engramId }: { engramId: string }) {
  const { data: ctx, isLoading } = useEngramContext(engramId);

  if (isLoading) return <p className="text-sm text-muted-foreground">Loading…</p>;
  if (!ctx) return null;

  return (
    <div className="space-y-4 text-sm">
      {/* Source episodes */}
      <div>
        <h3 className="font-medium mb-2">
          Source Episodes ({ctx.source_episodes.length})
        </h3>
        {ctx.source_episodes.length === 0 ? (
          <p className="text-muted-foreground">None.</p>
        ) : (
          <div className="space-y-2">
            {ctx.source_episodes.map((ep) => (
              <div key={ep.id} className="border rounded p-2 space-y-1">
                <p className="line-clamp-2">{ep.content}</p>
                <p className="text-xs text-muted-foreground">
                  {ep.author && <span>{ep.author} · </span>}
                  {relativeTime(ep.timestamp_event)}
                </p>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Linked entities */}
      <div>
        <h3 className="font-medium mb-2">
          Linked Entities ({ctx.linked_entities.length})
        </h3>
        <div className="flex flex-wrap gap-1.5">
          {ctx.linked_entities.length === 0 ? (
            <p className="text-muted-foreground">None.</p>
          ) : (
            ctx.linked_entities.map((ent) => (
              <Badge key={ent.id} variant="secondary" className="text-xs">
                {ent.name}
                {ent.type && (
                  <span className="ml-1 opacity-60">{ent.type}</span>
                )}
              </Badge>
            ))
          )}
        </div>
      </div>
    </div>
  );
}

function EngramDialog({
  engram,
  onClose,
}: {
  engram: Engram;
  onClose: () => void;
}) {
  const navigate = useNavigate();
  const isLabile =
    engram.labile_until && new Date(engram.labile_until) > new Date();

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-2xl max-h-[80vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <span>Engram</span>
            <span className="font-mono text-xs text-muted-foreground">
              {shortId(engram.id)}
            </span>
            {isLabile && (
              <span className="h-2 w-2 rounded-full bg-amber-400 animate-pulse" />
            )}
          </DialogTitle>
        </DialogHeader>

        <Tabs defaultValue="summary">
          <TabsList>
            <TabsTrigger value="summary">Summary</TabsTrigger>
            <TabsTrigger value="related">Related</TabsTrigger>
          </TabsList>

          <TabsContent value="summary" className="mt-4 space-y-3 text-sm">
            <p className="leading-relaxed">{engram.summary}</p>
            <div className="grid grid-cols-2 gap-2 text-xs text-muted-foreground">
              <div>
                Type:{" "}
                <Badge variant="outline" className="text-xs">
                  {engram.engram_type ?? "knowledge"}
                </Badge>
              </div>
              <div>Strength: {engram.strength}</div>
              <div>Activation: {engram.activation.toFixed(3)}</div>
              <div>Event: {relativeTime(engram.event_time)}</div>
              <div>Created: {relativeTime(engram.created_at)}</div>
              <div>Accessed: {relativeTime(engram.last_accessed)}</div>
            </div>
            <div className="flex gap-2 pt-2">
              <Button
                size="sm"
                variant="outline"
                onClick={() => {
                  onClose();
                  void navigate(`/graph?id=${engram.id}`);
                }}
              >
                <Network className="h-3.5 w-3.5 mr-1.5" /> Show in Graph
              </Button>
            </div>
          </TabsContent>

          <TabsContent value="related" className="mt-4">
            <EngramContextPanel engramId={engram.id} />
          </TabsContent>
        </Tabs>
      </DialogContent>
    </Dialog>
  );
}

export default function Engrams() {
  const [threshold, setThreshold] = useState(0);
  const [thresholdInput, setThresholdInput] = useState("0");
  const [selectedEngram, setSelectedEngram] = useState<Engram | null>(null);

  const { data: engrams, isLoading } = useEngrams({ threshold, limit: 100 });
  const deleteMut = useDeleteEngram();

  const eng = (engrams ?? []) as Engram[];

  const applyThreshold = () => {
    const v = parseFloat(thresholdInput);
    if (!isNaN(v) && v >= 0 && v <= 1) setThreshold(v);
  };

  return (
    <div className="p-6 space-y-4">
      <div className="flex items-center gap-3">
        <h1 className="text-2xl font-semibold">Engrams</h1>
        <div className="flex items-center gap-2 ml-auto">
          <Label htmlFor="threshold-input" className="text-sm whitespace-nowrap">
            Min activation
          </Label>
          <Input
            id="threshold-input"
            type="number"
            min={0}
            max={1}
            step={0.05}
            value={thresholdInput}
            onChange={(e) => setThresholdInput(e.target.value)}
            onBlur={applyThreshold}
            onKeyDown={(e) => e.key === "Enter" && applyThreshold()}
            className="w-24"
          />
        </div>
      </div>

      {isLoading ? (
        <p className="text-muted-foreground text-sm">Loading…</p>
      ) : eng.length === 0 ? (
        <p className="text-muted-foreground text-sm">No engrams found.</p>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {eng.map((e) => {
            const isLabile =
              e.labile_until && new Date(e.labile_until) > new Date();
            return (
              <Card
                key={e.id}
                className={`cursor-pointer hover:shadow-md transition-shadow border-l-4 ${
                  e.engram_type === "operational"
                    ? "border-l-amber-500"
                    : "border-l-blue-500"
                }`}
                onClick={() => setSelectedEngram(e)}
              >
                <CardHeader className="pb-2 pt-3 px-4">
                  <div className="flex items-center gap-2">
                    <Badge
                      variant={
                        e.engram_type === "operational" ? "secondary" : "default"
                      }
                      className="text-xs"
                    >
                      {e.engram_type ?? "knowledge"}
                    </Badge>
                    {isLabile && (
                      <span
                        className="h-2 w-2 rounded-full bg-amber-400 animate-pulse"
                        title="Labile"
                      />
                    )}
                    <span className="ml-auto text-xs text-muted-foreground font-mono">
                      {shortId(e.id)}
                    </span>
                  </div>
                </CardHeader>
                <CardContent className="px-4 pb-3 space-y-2">
                  <p className="text-sm line-clamp-3">{e.summary}</p>
                  <ActivationBar value={e.activation} />
                  <div className="flex items-center justify-between text-xs text-muted-foreground">
                    <span>strength {e.strength}</span>
                    <span>{relativeTime(e.event_time)}</span>
                  </div>
                  <div className="flex items-center gap-2 pt-1">
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-7 text-xs"
                      onClick={(ev) => {
                        ev.stopPropagation();
                        window.location.href = `/graph?id=${e.id}`;
                      }}
                    >
                      <Network className="h-3 w-3 mr-1" /> Graph
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-7 text-xs text-destructive hover:text-destructive"
                      onClick={(ev) => {
                        ev.stopPropagation();
                        if (confirm("Delete this engram?"))
                          deleteMut.mutate(e.id);
                      }}
                    >
                      <Trash2 className="h-3 w-3 mr-1" /> Delete
                    </Button>
                  </div>
                </CardContent>
              </Card>
            );
          })}
        </div>
      )}

      {selectedEngram && (
        <EngramDialog
          engram={selectedEngram}
          onClose={() => setSelectedEngram(null)}
        />
      )}
    </div>
  );
}
