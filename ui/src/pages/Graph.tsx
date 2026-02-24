import { useState, useCallback, useEffect } from "react";
import { useSearchParams } from "react-router";
import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  useNodesState,
  useEdgesState,
  type NodeMouseHandler,
  type Node,
  type Edge,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Search, X, RefreshCw } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import EngramNode from "@/components/graph/EngramNode";
import EntityNode from "@/components/graph/EntityNode";
import { searchEngrams, getEngram, getEngramContext } from "@/api/engrams";
import { buildGraphFromContexts } from "@/lib/graphBuilder";
import { layoutGraph } from "@/lib/graphLayout";
import { relativeTime, shortId } from "@/lib/format";
import { entityTypeColor } from "@/pages/Entities";
import type { Engram, EngramContext, EntityType } from "@/api/types";

const nodeTypes = {
  engramNode: EngramNode,
  entityNode: EntityNode,
};

// Fetch contexts for a set of engrams, then override ctx.engram with the
// level-8 version from the search results so node text uses summarized summaries.
async function fetchContexts(engrams: Engram[]): Promise<EngramContext[]> {
  const byId = new Map(engrams.map((e) => [e.id, e]));
  const results = await Promise.all(
    engrams.map((e) =>
      getEngramContext(e.id, true)
        .then((ctx) => ({ ...ctx, engram: byId.get(e.id) ?? ctx.engram }))
        .catch(() => null),
    ),
  );
  return results.filter((r): r is EngramContext => r !== null);
}

function EngramPopup({
  engram,
  context,
  onClose,
}: {
  engram: Engram;
  context: EngramContext | undefined;
  onClose: () => void;
}) {
  const isLabile =
    engram.labile_until && new Date(engram.labile_until) > new Date();
  const isOperational = engram.engram_type === "operational";

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2 text-base">
            <Badge
              variant={isOperational ? "secondary" : "default"}
              className="text-xs font-normal"
            >
              {isOperational ? "operational" : "knowledge"}
            </Badge>
            {isLabile && (
              <span
                className="h-2 w-2 rounded-full bg-amber-400 animate-pulse"
                title="Labile — in reconsolidation window"
              />
            )}
            <span className="ml-auto font-mono text-xs text-muted-foreground font-normal">
              {shortId(engram.id)}
            </span>
          </DialogTitle>
        </DialogHeader>

        {/* Summary */}
        <p className="text-sm leading-relaxed">{engram.summary}</p>

        {/* Metadata */}
        <div className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs text-muted-foreground border-t pt-3">
          <div className="flex items-center gap-2">
            <span>Activation</span>
            <div className="flex items-center gap-1 flex-1">
              <div className="h-1.5 bg-muted rounded-full flex-1">
                <div
                  className="h-1.5 bg-primary rounded-full"
                  style={{ width: `${Math.min(100, engram.activation * 100)}%` }}
                />
              </div>
              <span className="font-mono">{engram.activation.toFixed(2)}</span>
            </div>
          </div>
          <div>Strength: <span className="text-foreground">{engram.strength}</span></div>
          <div>Event: <span className="text-foreground">{relativeTime(engram.event_time)}</span></div>
          <div>Created: <span className="text-foreground">{relativeTime(engram.created_at)}</span></div>
        </div>

        {/* Linked entities */}
        {context && context.linked_entities.length > 0 && (
          <div className="border-t pt-3">
            <p className="text-xs font-medium text-muted-foreground mb-2">
              Linked entities ({context.linked_entities.length})
            </p>
            <div className="flex flex-wrap gap-1.5">
              {context.linked_entities.map((ent) => (
                <span
                  key={ent.id}
                  className={`inline-flex items-center gap-1 rounded-full px-2.5 py-0.5 text-xs font-medium ${entityTypeColor(ent.type as EntityType)}`}
                >
                  {ent.name}
                  {ent.type && (
                    <span className="opacity-60 text-[10px]">{ent.type}</span>
                  )}
                </span>
              ))}
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

export default function Graph() {
  const [searchParams] = useSearchParams();
  const [query, setQuery] = useState("");
  const [idInput, setIdInput] = useState("");
  const [loading, setLoading] = useState(false);

  // All contexts currently in the graph, keyed by engram ID for popup lookup
  const [contextMap, setContextMap] = useState<Map<string, EngramContext>>(new Map());
  const [selectedEngramId, setSelectedEngramId] = useState<string | null>(null);

  const [nodes, setNodes, onNodesChange] = useNodesState<Node>([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([]);

  const applyLayout = useCallback(
    (ctxs: EngramContext[]) => {
      const { nodes: raw, edges: rawEdges } = buildGraphFromContexts(ctxs);
      const { nodes: laid, edges: laidEdges } = layoutGraph(raw, rawEdges);
      setNodes(laid);
      setEdges(laidEdges);
    },
    [setNodes, setEdges],
  );

  const mergeContexts = useCallback(
    (newCtxs: EngramContext[]) => {
      setContextMap((prev) => {
        const next = new Map(prev);
        for (const c of newCtxs) next.set(c.engram.id, c);
        applyLayout(Array.from(next.values()));
        return next;
      });
    },
    [applyLayout],
  );

  // Handle ?id= on mount
  useEffect(() => {
    const id = searchParams.get("id");
    if (!id) return;
    setLoading(true);
    Promise.all([getEngram(id, true, 8), getEngramContext(id, true)])
      .then(([engram, ctx]) => mergeContexts([{ ...ctx, engram }]))
      .catch(() => {})
      .finally(() => setLoading(false));
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const handleSearch = async () => {
    if (!query.trim()) return;
    setLoading(true);
    try {
      const results = (await searchEngrams(query.trim(), 8, true, 8)) as Engram[];
      const ctxs = await fetchContexts(results);
      mergeContexts(ctxs);
    } finally {
      setLoading(false);
    }
  };

  const handleIdLoad = async () => {
    if (!idInput.trim()) return;
    setLoading(true);
    try {
      const [engram, ctx] = await Promise.all([
        getEngram(idInput.trim(), true, 8),
        getEngramContext(idInput.trim(), true),
      ]);
      mergeContexts([{ ...ctx, engram }]);
      setIdInput("");
    } finally {
      setLoading(false);
    }
  };

  const handleClear = () => {
    setContextMap(new Map());
    setNodes([]);
    setEdges([]);
    setQuery("");
    setIdInput("");
    setSelectedEngramId(null);
  };

  const handleRelayout = () => {
    applyLayout(Array.from(contextMap.values()));
  };

  const onNodeClick: NodeMouseHandler = useCallback((_evt, node) => {
    if (node.type === "engramNode") {
      setSelectedEngramId(node.id);
    }
  }, []);

  const selectedContext = selectedEngramId ? contextMap.get(selectedEngramId) : undefined;
  const selectedEngram = selectedContext?.engram ?? null;

  return (
    <div className="flex flex-col h-full">
      {/* Toolbar */}
      <div className="flex items-center gap-2 p-3 border-b bg-card flex-shrink-0">
        <div className="flex gap-1.5 flex-1 max-w-md">
          <Input
            placeholder="Search engrams…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && void handleSearch()}
            className="h-8"
          />
          <Button size="sm" onClick={() => void handleSearch()} disabled={loading}>
            <Search className="h-3.5 w-3.5" />
          </Button>
        </div>

        <div className="flex gap-1.5">
          <Input
            placeholder="Engram ID…"
            value={idInput}
            onChange={(e) => setIdInput(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && void handleIdLoad()}
            className="h-8 w-44"
          />
          <Button
            size="sm"
            variant="secondary"
            onClick={() => void handleIdLoad()}
            disabled={loading}
          >
            Load
          </Button>
        </div>

        <Button size="sm" variant="outline" onClick={handleRelayout} title="Re-layout">
          <RefreshCw className="h-3.5 w-3.5" />
        </Button>

        <Button size="sm" variant="outline" onClick={handleClear} title="Clear">
          <X className="h-3.5 w-3.5" />
        </Button>

        {loading && (
          <span className="text-xs text-muted-foreground animate-pulse">Loading…</span>
        )}
      </div>

      {/* Canvas */}
      <div className="flex-1">
        {nodes.length === 0 && !loading ? (
          <div className="flex items-center justify-center h-full text-muted-foreground text-sm">
            Search for engrams to populate the graph
          </div>
        ) : (
          <ReactFlow
            nodes={nodes}
            edges={edges}
            nodeTypes={nodeTypes}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onNodeClick={onNodeClick}
            fitView
            fitViewOptions={{ padding: 0.2 }}
            nodesDraggable
          >
            <Background />
            <Controls />
            <MiniMap />
          </ReactFlow>
        )}
      </div>

      {/* Engram popup */}
      {selectedEngram && (
        <EngramPopup
          engram={selectedEngram}
          context={selectedContext}
          onClose={() => setSelectedEngramId(null)}
        />
      )}
    </div>
  );
}
