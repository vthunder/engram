import {
  forceSimulation,
  forceLink,
  forceManyBody,
  forceCenter,
  forceCollide,
  type SimulationNodeDatum,
  type SimulationLinkDatum,
} from "d3-force";
import type { Node, Edge } from "@xyflow/react";

// Collision radius per node type — half the diagonal of the node bounding box,
// with a bit of padding so nodes don't overlap.
function collisionRadius(type: string | undefined): number {
  switch (type) {
    case "episodeNode": return 90;
    case "entityNode":  return 70;
    default:            return 130; // engramNode (192px wide)
  }
}

// Link rest-length per relationship type.
// Engram↔Episode and Engram↔Entity should sit close; engram↔engram further apart.
function linkDistance(_sourceType: string | undefined, targetType: string | undefined): number {
  if (targetType === "episodeNode" || targetType === "entityNode") return 220;
  return 300;
}

interface SimNode extends SimulationNodeDatum {
  id: string;
  nodeType: string | undefined;
}

export function layoutGraph(
  nodes: Node[],
  edges: Edge[],
): { nodes: Node[]; edges: Edge[] } {
  if (nodes.length === 0) return { nodes, edges };

  const simNodes: SimNode[] = nodes.map((n) => ({
    id: n.id,
    nodeType: n.type,
    // Spread initial positions so the simulation converges faster and doesn't
    // start all at the origin (which creates degenerate force directions).
    x: (Math.random() - 0.5) * 600,
    y: (Math.random() - 0.5) * 600,
  }));

  const idToSimNode = new Map(simNodes.map((n) => [n.id, n]));

  const simLinks: SimulationLinkDatum<SimNode>[] = edges.flatMap((e) => {
    const source = idToSimNode.get(e.source);
    const target = idToSimNode.get(e.target);
    if (!source || !target) return [];
    return [{ source, target }];
  });

  // Build a nodeType lookup keyed by id for link distance calculation
  const typeById = new Map(simNodes.map((n) => [n.id, n.nodeType]));

  const simulation = forceSimulation<SimNode>(simNodes)
    .force(
      "link",
      forceLink<SimNode, SimulationLinkDatum<SimNode>>(simLinks)
        .id((d) => d.id)
        .distance((link) => {
          const src = (link.source as SimNode).id;
          const tgt = (link.target as SimNode).id;
          return linkDistance(typeById.get(src), typeById.get(tgt));
        })
        .strength(0.4),
    )
    .force(
      "charge",
      forceManyBody<SimNode>()
        .strength((d) => (d.nodeType === "engramNode" ? -800 : -300))
        .distanceMax(800),
    )
    .force("center", forceCenter(0, 0).strength(0.05))
    .force(
      "collide",
      forceCollide<SimNode>()
        .radius((d) => collisionRadius(d.nodeType))
        .strength(0.8),
    )
    .stop();

  // Run synchronously to convergence (alphaMin default 0.001 ≈ 300 ticks).
  simulation.tick(400);

  const positioned = nodes.map((node) => {
    const sim = idToSimNode.get(node.id)!;
    return {
      ...node,
      position: {
        x: sim.x ?? 0,
        y: sim.y ?? 0,
      },
    };
  });

  return { nodes: positioned, edges };
}
