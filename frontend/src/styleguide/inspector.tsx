// The SELECT ELEMENT widget extracted from the monolithic StyleguideView.
// Wraps the index landing page so reviewers/agents can hover/click any
// element on the catalog and POST a structured selection packet to
// /api/design/selection. Feature pages don't get the inspector — they're
// focused single-element views.

import { useState } from "react";

import { authedFetch } from "../auth";
import {
  captionStyle,
  headStyle,
  inspectBoxFor,
  rowStyle,
  sectionStyle,
  selectionForElement,
  styleguideShellStyle,
  type DesignSelection,
  type InspectBox,
} from "./shared";

export function StyleguideInspector({ children }: { children: React.ReactNode }) {
  const [inspectMode, setInspectMode] = useState(false);
  const [hoverBox, setHoverBox] = useState<InspectBox | null>(null);
  const [selectedBox, setSelectedBox] = useState<InspectBox | null>(null);
  const [selection, setSelection] = useState<DesignSelection | null>(null);
  const [selectionStatus, setSelectionStatus] = useState("no selection yet");

  function handleInspectMove(event: React.MouseEvent<HTMLDivElement>) {
    if (!inspectMode || !(event.target instanceof HTMLElement)) return;
    const target = event.target.closest<HTMLElement>("[data-inspectable], button, a, [role], code, pre, li, section");
    if (!target || target.closest("[data-inspector-control]")) {
      setHoverBox(null);
      return;
    }
    setHoverBox(inspectBoxFor(target));
  }

  function handleInspectClick(event: React.MouseEvent<HTMLDivElement>) {
    if (!inspectMode || !(event.target instanceof HTMLElement)) return;
    if (event.target.closest("[data-inspector-control]")) return;

    const target = event.target.closest<HTMLElement>("[data-inspectable], button, a, [role], code, pre, li, section");
    if (!target) return;

    event.preventDefault();
    event.stopPropagation();

    const nextSelection = selectionForElement(target, event.currentTarget);
    setSelection(nextSelection);
    setSelectedBox(inspectBoxFor(target));
    setSelectionStatus("posting selection...");

    void authedFetch("/api/design/selection", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(nextSelection),
    })
      .then((res) => {
        setSelectionStatus(res.ok ? "selection posted" : `post failed: ${res.status}`);
      })
      .catch((error: unknown) => {
        setSelectionStatus(error instanceof Error ? `post failed: ${error.message}` : "post failed");
      });
  }

  return (
    <div
      onMouseMove={handleInspectMove}
      onMouseLeave={() => setHoverBox(null)}
      onClickCapture={handleInspectClick}
      style={styleguideShellStyle}
    >
      <section
        data-inspector-control
        style={{
          ...sectionStyle,
          position: "sticky",
          top: 0,
          zIndex: 30,
          background: "rgba(23,23,23,0.94)",
          backdropFilter: "blur(8px)",
        }}
      >
        <h2 style={headStyle}>select element</h2>
        <p style={captionStyle}>
          Toggle inspection, hover to confirm the target, then click a UI
          element. The styleguide posts a structured selection packet to
          <code> /api/design/selection</code>; agents can read it from
          <code> /api/design/selection/latest</code>.
        </p>
        <div style={{ display: "grid", gap: 12 }}>
          <div style={rowStyle}>
            <button
              className={inspectMode ? "btn-primary" : "btn-secondary"}
              type="button"
              onClick={() => {
                setInspectMode((value) => !value);
                setHoverBox(null);
              }}
            >
              {inspectMode ? "selecting..." : "Select element"}
            </button>
            <button
              className="link-button"
              type="button"
              onClick={() => {
                setSelection(null);
                setSelectedBox(null);
                setSelectionStatus("no selection yet");
              }}
            >
              Clear
            </button>
            <span style={{ fontSize: "var(--text-xs)", color: "var(--text-faint)" }}>
              {selectionStatus}
            </span>
          </div>
          {selection && (
            <pre
              className="error"
              style={{
                margin: 0,
                maxHeight: 220,
                overflow: "auto",
                background: "var(--bg-base)",
                color: "var(--text-body)",
              }}
            >
              {JSON.stringify(selection, null, 2)}
            </pre>
          )}
        </div>
      </section>
      {children}
      {inspectMode && hoverBox && (
        <div
          aria-hidden="true"
          style={{
            position: "fixed",
            top: hoverBox.top,
            left: hoverBox.left,
            width: hoverBox.width,
            height: hoverBox.height,
            border: "1px solid var(--cyan)",
            boxShadow: "0 0 0 1px rgba(103,232,249,0.24)",
            pointerEvents: "none",
            zIndex: 1000,
          }}
        >
          <span
            style={{
              position: "absolute",
              left: 0,
              top: -22,
              maxWidth: 240,
              overflow: "hidden",
              textOverflow: "ellipsis",
              whiteSpace: "nowrap",
              borderRadius: "var(--radius-sm)",
              background: "var(--cyan)",
              color: "#082f36",
              fontSize: "var(--text-xs)",
              padding: "2px 6px",
            }}
          >
            {hoverBox.label}
          </span>
        </div>
      )}
      {selectedBox && (
        <div
          aria-hidden="true"
          style={{
            position: "fixed",
            top: selectedBox.top,
            left: selectedBox.left,
            width: selectedBox.width,
            height: selectedBox.height,
            border: "2px solid var(--status-online)",
            pointerEvents: "none",
            zIndex: 999,
          }}
        />
      )}
    </div>
  );
}
