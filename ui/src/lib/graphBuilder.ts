import type { Node, Edge } from "@xyflow/react";
import type { EngramContext, Engram, Entity } from "@/api/types";

export interface GraphData {
  nodes: Node[];
  edges: Edge[];
}

export function buildGraphFromContexts(contexts: EngramContext[]): GraphData {
  const nodeMap = new Map<string, Node>();
  const edgeMap = new Map<string, Edge>();

  function addEngramNode(engram: Engram) {
    if (nodeMap.has(engram.id)) return;
    nodeMap.set(engram.id, {
      id: engram.id,
      type: "engramNode",
      position: { x: 0, y: 0 },
      data: { engram },
    });
  }

  function addEntityNode(entity: Entity) {
    if (nodeMap.has(entity.id)) return;
    nodeMap.set(entity.id, {
      id: entity.id,
      type: "entityNode",
      position: { x: 0, y: 0 },
      data: { entity },
    });
  }

  function addEdge(source: string, target: string) {
    const id = `${source}->${target}`;
    if (edgeMap.has(id)) return;
    edgeMap.set(id, {
      id,
      source,
      target,
      type: "smoothstep",
      style: { stroke: "#7c3aed", strokeWidth: 1.5 },
    });
  }

  for (const ctx of contexts) {
    addEngramNode(ctx.engram);

    for (const entity of ctx.linked_entities) {
      addEntityNode(entity);
      addEdge(ctx.engram.id, entity.id);
    }
  }

  return {
    nodes: Array.from(nodeMap.values()),
    edges: Array.from(edgeMap.values()),
  };
}
