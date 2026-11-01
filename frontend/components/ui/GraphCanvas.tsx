"use client";
import {
  forwardRef,
  useEffect,
  useImperativeHandle,
  useMemo,
  useRef,
  type ReactNode,
  type MouseEvent,
} from "react";
import {
  ReactFlow,
  useUpdateNodeInternals,
  Background,
  BackgroundVariant,
  Controls,
  MiniMap,
  Handle,
  Position,
  MarkerType,
  type Node,
  type NodeProps,
  type Edge,
  type ReactFlowInstance,
} from "@xyflow/react";
export type CanvasNode = {
  id: string;
  x: number;
  y: number;
  content: ReactNode;
  selected?: boolean;
  inputs?: boolean;
  outputs?: string[];
  dragHandle?: string;
};
export type CanvasEdge = {
  id: string;
  from: string;
  to: string;
  port?: string;
  label?: string;
  status?: string;
};
export type GraphCanvasHandle = { fit: () => void };
type FlowNode = Node<{
  content: ReactNode;
  inputs: boolean;
  outputs: string[];
}>;
function CanvasCard({ id, data }: NodeProps<FlowNode>) {
  const refresh = useUpdateNodeInternals();
  const portKey = data.outputs.join("|");
  useEffect(() => refresh(id), [id, data.inputs, portKey, refresh]);
  return (
    <>
      {data.inputs && <Handle type="target" position={Position.Left} id="in" />}
      {data.content}
      {data.outputs.map((port, index) => (
        <Handle
          key={port}
          type="source"
          id={port}
          position={Position.Right}
          style={{ top: `${((index + 1) / (data.outputs.length + 1)) * 100}%` }}
        />
      ))}
    </>
  );
}
const nodeTypes = { card: CanvasCard };
/** Shared, controlled graph surface; persisted positions and connections remain owned by callers. */
export const GraphCanvas = forwardRef<
  GraphCanvasHandle,
  {
    nodes: CanvasNode[];
    edges: CanvasEdge[];
    editable?: boolean;
    zoom?: number;
    onZoom?: (zoom: number) => void;
    onSelect?: (ids: string[]) => void;
    onPositions?: (positions: Record<string, { x: number; y: number }>) => void;
    onDragStart?: () => void;
    onConnect?: (from: string, to: string, port: string) => void;
    canConnect?: (from: string, to: string, port: string) => boolean;
    onContext?: (event: MouseEvent, id: string) => void;
    onAdd?: (kind: string, position: { x: number; y: number }) => void;
    snap?: boolean;
    label: string;
  }
>(function GraphCanvas(
  {
    nodes,
    edges,
    editable = false,
    zoom,
    onZoom,
    onSelect,
    onPositions,
    onDragStart,
    onConnect,
    canConnect,
    onContext,
    onAdd,
    snap = false,
    label,
  },
  ref,
) {
  const api = useRef<ReactFlowInstance<FlowNode, Edge> | null>(null);
  const flowNodes = useMemo<FlowNode[]>(
    () =>
      nodes.map((node) => ({
        id: node.id,
        type: "card",
        position: { x: node.x, y: node.y },
        selected: node.selected,
        dragHandle: node.dragHandle,
        data: {
          content: node.content,
          inputs: !!node.inputs,
          outputs: node.outputs || [],
        },
      })),
    [nodes],
  );
  const flowEdges = useMemo<Edge[]>(
    () =>
      edges.map((edge) => ({
        id: edge.id,
        className: "design-edge",
        source: edge.from,
        target: edge.to,
        sourceHandle: edge.port,
        markerEnd: { type: MarkerType.ArrowClosed, width: 18, height: 18 },
        type: "smoothstep",
        label: edge.label,
        labelStyle: { fill: "var(--color-ink)" },
        labelBgStyle: { fill: "var(--color-paper)" },
        style: {
          stroke:
            edge.status === "succeeded"
              ? "var(--color-success)"
              : edge.status === "failed"
                ? "var(--color-danger)"
                : "var(--color-muted)",
          strokeWidth: 1.5,
        },
      })),
    [edges],
  );
  useImperativeHandle(
    ref,
    () => ({
      fit: () => void api.current?.fitView({ padding: 0.22, maxZoom: 1 }),
    }),
    [],
  );
  useEffect(() => {
    if (
      zoom !== undefined &&
      api.current &&
      Math.abs(api.current.getZoom() - zoom) > 0.001
    )
      void api.current.zoomTo(zoom);
  }, [zoom]);
  return (
    <div
      className="flow-surface"
      role="region"
      aria-label={label}
      onDragOver={(event) => {
        if (editable) {
          event.preventDefault();
          event.dataTransfer.dropEffect = "copy";
        }
      }}
      onDrop={(event) => {
        event.preventDefault();
        const kind = event.dataTransfer.getData("application/kionga-component");
        if (editable && kind && api.current)
          onAdd?.(
            kind,
            api.current.screenToFlowPosition({
              x: event.clientX,
              y: event.clientY,
            }),
          );
      }}
    >
      <ReactFlow<FlowNode>
        colorMode="light"
        nodes={flowNodes}
        edges={flowEdges}
        nodeTypes={nodeTypes}
        nodesDraggable={editable}
        nodesConnectable={editable}
        elementsSelectable
        deleteKeyCode={null}
        minZoom={0.2}
        maxZoom={2}
        fitView
        fitViewOptions={{ padding: 0.22, maxZoom: 1 }}
        snapToGrid={snap}
        snapGrid={[20, 20]}
        nodeExtent={[
          [0, 0],
          [20000, 20000],
        ]}
        onInit={(instance) => {
          api.current = instance;
        }}
        onMoveEnd={(_, viewport) => onZoom?.(viewport.zoom)}
        onNodeClick={(_, node) => onSelect?.([node.id])}
        onPaneClick={() => onSelect?.([])}
        onNodeContextMenu={(event, node) => {
          event.preventDefault();
          onContext?.(event, node.id);
        }}
        onNodeDragStart={() => onDragStart?.()}
        onNodesChange={(changes) => {
          const positions = Object.fromEntries(
            changes.flatMap((change) =>
              change.type === "position" && change.position
                ? [[change.id, change.position]]
                : [],
            ),
          );
          if (Object.keys(positions).length) onPositions?.(positions);
          const selection = changes.filter(
            (change) => change.type === "select",
          );
          if (selection.length) {
            const next = new Set(
              nodes.filter((n) => n.selected).map((n) => n.id),
            );
            for (const change of selection)
              if (change.type === "select")
                change.selected ? next.add(change.id) : next.delete(change.id);
            onSelect?.([...next]);
          }
        }}
        onConnect={(edge) =>
          onConnect?.(edge.source, edge.target, edge.sourceHandle || "out")
        }
        isValidConnection={(edge) =>
          canConnect?.(edge.source, edge.target, edge.sourceHandle || "out") ??
          true
        }
        panOnDrag
        selectionOnDrag={false}
        preventScrolling
      >
        <Background
          variant={BackgroundVariant.Dots}
          gap={20}
          size={1.2}
          color="var(--color-rule-2)"
        />
        <Controls showInteractive={false} />
        <MiniMap
          pannable
          zoomable
          nodeColor="var(--color-accent-wash)"
          nodeStrokeColor="var(--color-accent-deep)"
          maskColor="var(--color-overlay)"
        />
      </ReactFlow>
    </div>
  );
});
