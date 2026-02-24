import { memo } from "react";
import { Handle, Position } from "@xyflow/react";
import type { Entity } from "@/api/types";
import { entityTypeColor } from "@/pages/Entities";
import type { EntityType } from "@/api/types";

interface EntityNodeData {
  entity: Entity;
}

function EntityNode({ data }: { data: EntityNodeData }) {
  const { entity } = data;
  const colorClass = entityTypeColor(entity.type as EntityType);

  return (
    <div
      className={`rounded-full border px-3 py-1 text-xs font-medium shadow-sm whitespace-nowrap ${colorClass}`}
    >
      <Handle type="target" position={Position.Left} className="!opacity-0" />
      {entity.name}
      {entity.type && (
        <span className="ml-1.5 opacity-60 text-[10px]">{entity.type}</span>
      )}
      <Handle type="source" position={Position.Right} className="!opacity-0" />
    </div>
  );
}

export default memo(EntityNode);
