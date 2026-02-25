import { useState } from "react";
import { Network, Trash2 } from "lucide-react";
import { useNavigate } from "react-router";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useEngrams, useEngramContext, useEngramChildren, useEngramAtLevel, useDeleteEngram } from "@/hooks/useEngrams";
import { relativeTime, shortId } from "@/lib/format";
import type { Engram } from "@/api/types";

const DEPTH_LABELS: Record<number, string> = {
  0: "L1",
  1: "L2",
  2: "L3",
  3: "L4",
  4: "L5",
};

function depthLabel(depth: number): string {
  return DEPTH_LABELS[depth] ?? `L${depth + 1}`;
}

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

function EngramContextPanel({ engram, level }: { engram: Engram; level: number }) {
  const isHigherDepth = (engram.depth ?? 0) > 0;
  const { data: ctx, isLoading: ctxLoading } = useEngramContext(isHigherDepth ? "" : engram.id);
  const { data: children, isLoading: childrenLoading } = useEngramChildren(isHigherDepth ? engram.id : "", level < 0 ? 0 : level);

  const isLoading = isHigherDepth ? childrenLoading : ctxLoading;
  if (isLoading) return <p className="text-sm text-muted-foreground">Loading…</p>;

  // L2+ engram: show source engrams
  if (isHigherDepth) {
    const childEngrams = (children?.children ?? []) as Engram[];
    return (
      <div className="space-y-4 text-sm">
        <div>
          <h3 className="font-medium mb-2">
            Source Engrams ({childEngrams.length})
          </h3>
          {childEngrams.length === 0 ? (
            <p className="text-muted-foreground">None.</p>
          ) : (
            <div className="space-y-2">
              {childEngrams.map((e) => (
                <div key={e.id} className="border rounded p-2 space-y-1">
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-xs text-muted-foreground">{shortId(e.id)}</span>
                    {(e.depth ?? 0) > 0 && (
                      <Badge variant="outline" className="text-xs font-mono">
                        {depthLabel(e.depth ?? 0)}
                      </Badge>
                    )}
                  </div>
                  <p className="line-clamp-3">{e.summary}</p>
                  <p className="text-xs text-muted-foreground">{relativeTime(e.event_time)}</p>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    );
  }

  // L1 engram: show source episodes + linked entities
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

const SUMMARY_LEVELS = [
  { value: 0, label: "Default" },
  { value: -1, label: "Full" },
  { value: 4, label: "C4" },
  { value: 8, label: "C8" },
  { value: 16, label: "C16" },
  { value: 32, label: "C32" },
  { value: 64, label: "C64" },
];

function EngramDialog({
  engram,
  onClose,
}: {
  engram: Engram;
  onClose: () => void;
}) {
  const navigate = useNavigate();
  const [level, setLevel] = useState(0);
  const isLabile =
    engram.labile_until && new Date(engram.labile_until) > new Date();

  const apiLevel = level < 0 ? 0 : level;
  const { data: leveledEngram } = useEngramAtLevel(level !== 0 ? engram.id : "", apiLevel);
  const displaySummary = level !== 0 ? (leveledEngram?.summary ?? engram.summary) : engram.summary;

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
          <div className="flex items-center justify-between">
            <TabsList>
              <TabsTrigger value="summary">Summary</TabsTrigger>
              <TabsTrigger value="related">Related</TabsTrigger>
            </TabsList>
            <Select
              value={String(level)}
              onValueChange={(v) => setLevel(parseInt(v))}
            >
              <SelectTrigger className="w-24 h-8 text-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {SUMMARY_LEVELS.map((l) => (
                  <SelectItem key={l.value} value={String(l.value)} className="text-xs">
                    {l.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <TabsContent value="summary" className="mt-4 space-y-3 text-sm">
            <p className="leading-relaxed">{displaySummary}</p>
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
            <EngramContextPanel engram={engram} level={level} />
          </TabsContent>
        </Tabs>
      </DialogContent>
    </Dialog>
  );
}

export default function Engrams() {
  const [threshold, setThreshold] = useState(0);
  const [thresholdInput, setThresholdInput] = useState("0");
  const [depth, setDepth] = useState<number | undefined>(undefined);
  const [selectedEngram, setSelectedEngram] = useState<Engram | null>(null);

  const { data: engrams, isLoading } = useEngrams({ threshold, limit: 500, depth });
  const deleteMut = useDeleteEngram();

  const eng = (engrams ?? []) as Engram[];

  const applyThreshold = () => {
    const v = parseFloat(thresholdInput);
    if (!isNaN(v) && v >= 0 && v <= 1) setThreshold(v);
  };

  // Derive available depths from loaded data (only when showing all depths)
  const availableDepths = depth === undefined
    ? [...new Set(eng.map((e) => e.depth ?? 0))].sort((a, b) => a - b)
    : [];

  return (
    <div className="p-6 space-y-4">
      <div className="flex items-center gap-3 flex-wrap">
        <h1 className="text-2xl font-semibold">Engrams</h1>
        <div className="flex items-center gap-3 ml-auto flex-wrap">
          <div className="flex items-center gap-2">
            <Label className="text-sm whitespace-nowrap">Depth</Label>
            <Select
              value={depth === undefined ? "all" : String(depth)}
              onValueChange={(v) => setDepth(v === "all" ? undefined : parseInt(v))}
            >
              <SelectTrigger className="w-32">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All depths</SelectItem>
                {depth === undefined
                  ? availableDepths.map((d) => (
                      <SelectItem key={d} value={String(d)}>
                        {depthLabel(d)}
                      </SelectItem>
                    ))
                  : Array.from({ length: Math.max(5, (depth ?? 0) + 1) }, (_, i) => (
                      <SelectItem key={i} value={String(i)}>
                        {depthLabel(i)}
                      </SelectItem>
                    ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex items-center gap-2">
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
                    {(e.depth ?? 0) > 0 && (
                      <Badge variant="outline" className="text-xs font-mono">
                        {depthLabel(e.depth ?? 0)}
                      </Badge>
                    )}
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
