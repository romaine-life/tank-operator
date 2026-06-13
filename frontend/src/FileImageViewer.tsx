import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import {
  Maximize2Icon,
  ZoomInIcon,
  ZoomOutIcon,
} from "lucide-react";
import {
  MAX_SCALE,
  MIN_SCALE,
  clampScale,
  computeFitScale,
  formatZoomPercent,
  scalesEqual,
  wheelZoomFactor,
  zoomBy,
  zoomIn as zoomInScale,
  zoomOut as zoomOutScale,
  type Size,
} from "./imageZoom";

interface FileImageViewerProps {
  src: string;
  alt: string;
  openHref?: string;
}

interface PendingScroll {
  /** Horizontal focal point as a fraction (0..1) of the scaled image width. */
  fx: number;
  /** Vertical focal point as a fraction (0..1) of the scaled image height. */
  fy: number;
  /** Pointer x within the viewport, kept anchored across the zoom change. */
  px: number;
  /** Pointer y within the viewport, kept anchored across the zoom change. */
  py: number;
}

const NO_SIZE: Size = { width: 0, height: 0 };

type MouseClickLike = Pick<
  MouseEvent,
  "button" | "metaKey" | "ctrlKey" | "shiftKey" | "altKey"
>;

function isPlainLeftClick(event: MouseClickLike): boolean {
  return (
    event.button === 0 &&
    !event.metaKey &&
    !event.ctrlKey &&
    !event.shiftKey &&
    !event.altKey
  );
}

/**
 * Zoomable preview for workspace image/screenshot files.
 *
 * Two display modes:
 *  - "fit": image is scaled to an inspectable contained size and tracks
 *    container resizes.
 *  - "zoom": image is rendered at `naturalSize * scale` pixels and the
 *    surrounding viewport scrolls/pans, so users can inspect detail.
 *
 * Interactions: +/-/fit toolbar buttons, mouse wheel to zoom toward the
 * cursor, double-click to toggle fit vs. 100%, and click-drag to pan when the
 * image overflows the viewport. Pure zoom math lives in `./imageZoom`.
 */
export function FileImageViewer({ src, alt, openHref }: FileImageViewerProps) {
  const wrapRef = useRef<HTMLDivElement | null>(null);
  const [naturalSize, setNaturalSize] = useState<Size>(NO_SIZE);
  const [containerSize, setContainerSize] = useState<Size>(NO_SIZE);
  // `fit === true` means "auto-contain"; otherwise render at `scale` pixels.
  const [fit, setFit] = useState(true);
  const [scale, setScale] = useState(1);
  const pendingScrollRef = useRef<PendingScroll | null>(null);

  // Reset zoom whenever a different image is selected.
  useEffect(() => {
    setFit(true);
    setScale(1);
    setNaturalSize(NO_SIZE);
    pendingScrollRef.current = null;
  }, [src]);

  // Track the viewport size so fit-mode stays responsive and zoom-to-cursor
  // math has an up-to-date container box.
  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return undefined;
    const update = () =>
      setContainerSize({ width: el.clientWidth, height: el.clientHeight });
    update();
    if (typeof ResizeObserver === "undefined") return undefined;
    const ro = new ResizeObserver(update);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const fitScale = computeFitScale(naturalSize, containerSize);
  const effectiveScale = fit ? fitScale : scale;
  const canZoomIn = effectiveScale < MAX_SCALE - 1e-6;
  const canZoomOut = effectiveScale > MIN_SCALE + 1e-6;

  // After a scale change re-renders the image at its new pixel size, restore
  // the focal point captured at interaction time so the spot under the cursor
  // (or the viewport centre) stays put.
  useLayoutEffect(() => {
    const wrap = wrapRef.current;
    const pending = pendingScrollRef.current;
    if (!wrap || !pending) return;
    pendingScrollRef.current = null;
    const scaledWidth = naturalSize.width * effectiveScale;
    const scaledHeight = naturalSize.height * effectiveScale;
    wrap.scrollLeft = pending.fx * scaledWidth - pending.px;
    wrap.scrollTop = pending.fy * scaledHeight - pending.py;
  }, [scale, fit, effectiveScale, naturalSize.width, naturalSize.height]);

  /**
   * Capture the focal point (under `clientX/clientY`, or the viewport centre)
   * before applying `nextScale`, then switch into zoom mode.
   */
  const applyScale = useCallback(
    (nextScale: number, clientX?: number, clientY?: number) => {
      const wrap = wrapRef.current;
      const target = clampScale(nextScale);
      if (wrap && naturalSize.width > 0 && naturalSize.height > 0) {
        const rect = wrap.getBoundingClientRect();
        const px =
          clientX != null ? clientX - rect.left : wrap.clientWidth / 2;
        const py =
          clientY != null ? clientY - rect.top : wrap.clientHeight / 2;
        const curWidth = naturalSize.width * effectiveScale;
        const curHeight = naturalSize.height * effectiveScale;
        const fx = curWidth > 0 ? (wrap.scrollLeft + px) / curWidth : 0;
        const fy = curHeight > 0 ? (wrap.scrollTop + py) / curHeight : 0;
        pendingScrollRef.current = { fx, fy, px, py };
      }
      // Returning to (or below) the fit baseline drops back into responsive
      // fit-mode so window resizes keep working.
      if (target <= fitScale + 1e-6) {
        setFit(true);
        setScale(target);
        pendingScrollRef.current = null;
      } else {
        setFit(false);
        setScale(target);
      }
    },
    [effectiveScale, fitScale, naturalSize.height, naturalSize.width],
  );

  const handleZoomIn = useCallback(() => {
    applyScale(zoomInScale(effectiveScale));
  }, [applyScale, effectiveScale]);

  const handleZoomOut = useCallback(() => {
    applyScale(zoomOutScale(effectiveScale));
  }, [applyScale, effectiveScale]);

  const handleFit = useCallback(() => {
    pendingScrollRef.current = null;
    setFit(true);
    setScale(1);
  }, []);

  const handleWheel = useCallback(
    (e: React.WheelEvent<HTMLDivElement>) => {
      const factor = wheelZoomFactor(e.deltaY, e.deltaMode);
      if (scalesEqual(factor, 1)) return;
      e.preventDefault();
      applyScale(zoomBy(effectiveScale, factor), e.clientX, e.clientY);
    },
    [applyScale, effectiveScale],
  );

  const handleDoubleClick = useCallback(
    (e: React.MouseEvent<HTMLDivElement>) => {
      if (fit) {
        applyScale(1, e.clientX, e.clientY);
      } else {
        handleFit();
      }
    },
    [applyScale, fit, handleFit],
  );

  // Click-drag panning when the scaled image overflows the viewport.
  const dragState = useRef<{
    startX: number;
    startY: number;
    scrollLeft: number;
    scrollTop: number;
  } | null>(null);
  const [dragging, setDragging] = useState(false);

  const overflowing =
    naturalSize.width * effectiveScale > containerSize.width + 1 ||
    naturalSize.height * effectiveScale > containerSize.height + 1;

  const handlePointerDown = useCallback(
    (e: React.PointerEvent<HTMLDivElement>) => {
      const wrap = wrapRef.current;
      if (!wrap || !overflowing || e.button !== 0) return;
      dragState.current = {
        startX: e.clientX,
        startY: e.clientY,
        scrollLeft: wrap.scrollLeft,
        scrollTop: wrap.scrollTop,
      };
      setDragging(true);
      wrap.setPointerCapture(e.pointerId);
    },
    [overflowing],
  );

  const handlePointerMove = useCallback(
    (e: React.PointerEvent<HTMLDivElement>) => {
      const wrap = wrapRef.current;
      const drag = dragState.current;
      if (!wrap || !drag) return;
      wrap.scrollLeft = drag.scrollLeft - (e.clientX - drag.startX);
      wrap.scrollTop = drag.scrollTop - (e.clientY - drag.startY);
    },
    [],
  );

  const endDrag = useCallback((e: React.PointerEvent<HTMLDivElement>) => {
    if (!dragState.current) return;
    dragState.current = null;
    setDragging(false);
    const wrap = wrapRef.current;
    if (wrap?.hasPointerCapture(e.pointerId)) {
      wrap.releasePointerCapture(e.pointerId);
    }
  }, []);

  const imgStyle: React.CSSProperties = fit
    ? {
        width: `${naturalSize.width * fitScale}px`,
        height: `${naturalSize.height * fitScale}px`,
        maxWidth: "100%",
        maxHeight: "100%",
        objectFit: "contain",
      }
    : {
        width: `${naturalSize.width * scale}px`,
        height: `${naturalSize.height * scale}px`,
        maxWidth: "none",
        maxHeight: "none",
      };

  const wrapClassName = [
    "run-files-viewer-image-wrap",
    overflowing ? "is-pannable" : "",
    dragging ? "is-dragging" : "",
  ]
    .filter(Boolean)
    .join(" ");

  const image = (
    <img
      className="run-files-viewer-image"
      alt={alt}
      src={src}
      draggable={false}
      style={imgStyle}
      onLoad={(e) => {
        const img = e.currentTarget;
        setNaturalSize({
          width: img.naturalWidth,
          height: img.naturalHeight,
        });
      }}
    />
  );

  return (
    <div className="run-files-viewer-image-stage">
      <div
        ref={wrapRef}
        className={wrapClassName}
        onWheel={handleWheel}
        onDoubleClick={handleDoubleClick}
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
        onPointerUp={endDrag}
        onPointerCancel={endDrag}
      >
        {openHref ? (
          <a
            className="run-files-viewer-image-link"
            href={openHref}
            target="_blank"
            rel="noreferrer"
            draggable={false}
            onClick={(e) => {
              if (isPlainLeftClick(e)) e.preventDefault();
            }}
            onDragStart={(e) => e.preventDefault()}
          >
            {image}
          </a>
        ) : (
          image
        )}
      </div>
      <div className="run-files-image-zoom" role="group" aria-label="Image zoom">
        <button
          type="button"
          className="run-files-image-zoom-btn"
          onClick={handleZoomOut}
          disabled={!canZoomOut}
          title="Zoom out"
          aria-label="Zoom out"
        >
          <ZoomOutIcon size={14} aria-hidden="true" />
        </button>
        <span
          className="run-files-image-zoom-level"
          aria-live="polite"
          title="Current zoom (scroll to zoom, double-click image to toggle)"
        >
          {formatZoomPercent(effectiveScale)}%
        </span>
        <button
          type="button"
          className="run-files-image-zoom-btn"
          onClick={handleZoomIn}
          disabled={!canZoomIn}
          title="Zoom in"
          aria-label="Zoom in"
        >
          <ZoomInIcon size={14} aria-hidden="true" />
        </button>
        <button
          type="button"
          className="run-files-image-zoom-btn"
          onClick={handleFit}
          disabled={fit && scalesEqual(effectiveScale, fitScale)}
          title="Fit to view"
          aria-label="Fit image to view"
        >
          <Maximize2Icon size={14} aria-hidden="true" />
        </button>
      </div>
    </div>
  );
}

export default FileImageViewer;
