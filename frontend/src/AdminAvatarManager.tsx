import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import type { PointerEvent as ReactPointerEvent } from "react";
import {
  ImagePlusIcon,
  Loader2Icon,
  Trash2Icon,
  UploadIcon,
} from "lucide-react";
import { authedFetch } from "./auth";
import {
  type AvatarCrop,
  type AvatarCropDragOffset,
  avatarCropContainsPoint,
  avatarCropDragOffset,
  avatarCropFromImagePoint,
  clampAvatarCrop,
  cropToSourceRect,
} from "./adminAvatarCrop";
import { openAvatarPreview } from "./avatarPreview";

type AvatarKind = "agent" | "system";

type AvatarEntry = {
  id: string;
  kind: AvatarKind;
  name: string;
  avatar_url: string;
  backing_url: string;
  crop: AvatarCrop;
  created_by: string;
  created_at: string;
};

type AvatarView = AvatarEntry & {
  avatarSrc: string | null;
  imageError: string | null;
};

type AvatarDeckEntry = {
  position: number;
  avatar_id: string;
  name: string;
  avatar_url?: string;
  used: boolean;
  used_session_id?: string;
  used_at?: string;
  available: boolean;
};

type AvatarDeckKind = {
  kind: AvatarKind;
  cycle: number;
  entries: AvatarDeckEntry[];
};

type AvatarUploadErrorBody = {
  detail?: unknown;
  code?: unknown;
  attempt_id?: unknown;
};

type ImageRect = {
  left: number;
  top: number;
  width: number;
  height: number;
};

type PendingCropPlacement = {
  pointerId: number;
  x: number;
  y: number;
  moved: boolean;
};

const defaultCrop: AvatarCrop = {
  center_x: 0.5,
  center_y: 0.5,
  size: 0.42,
};

const avatarCanvasSize = 512;
const cropDragHitSlopPx = 18;
const cropPlacementMoveThresholdPx = 6;

function extensionForImageType(type: string): string {
  switch (type) {
    case "image/jpeg":
      return "jpg";
    case "image/webp":
      return "webp";
    case "image/gif":
      return "gif";
    case "image/avif":
      return "avif";
    case "image/bmp":
      return "bmp";
    default:
      return "png";
  }
}

function namedClipboardImage(file: File): File {
  if (file.name) return file;
  const extension = extensionForImageType(file.type);
  return new File([file], `pasted-avatar.${extension}`, {
    type: file.type || "image/png",
    lastModified: Date.now(),
  });
}

function imageFileFromClipboard(data: DataTransfer): File | null {
  for (const item of Array.from(data.items)) {
    if (item.kind !== "file" || !item.type.startsWith("image/")) continue;
    const file = item.getAsFile();
    if (file) return namedClipboardImage(file);
  }
  for (const file of Array.from(data.files)) {
    if (file.type.startsWith("image/")) return namedClipboardImage(file);
  }
  return null;
}

function isAvatarEntry(value: unknown): value is AvatarEntry {
  if (!value || typeof value !== "object") return false;
  const entry = value as Record<string, unknown>;
  return (
    typeof entry.id === "string" &&
    (entry.kind === "agent" || entry.kind === "system") &&
    typeof entry.name === "string" &&
    typeof entry.avatar_url === "string" &&
    typeof entry.backing_url === "string"
  );
}

export async function fetchAvatarViews(): Promise<AvatarView[]> {
  const res = await authedFetch("/api/avatars");
  if (!res.ok) throw new Error(`avatar list failed: ${res.status}`);
  const body = (await res.json()) as { entries?: unknown };
  const entries = Array.isArray(body.entries)
    ? body.entries.filter(isAvatarEntry)
    : [];
  const views = await Promise.all(entries.map(async (entry) => {
    const imageRes = await authedFetch(entry.avatar_url);
    if (!imageRes.ok) {
      return {
        ...entry,
        avatarSrc: null,
        imageError: `avatar image failed: ${imageRes.status}`,
      };
    }
    return {
      ...entry,
      avatarSrc: URL.createObjectURL(await imageRes.blob()),
      imageError: null,
    };
  }));
  return views;
}

type AdminAvatarManagerProps = {
  onCatalogChanged?: () => void | Promise<void>;
};

function isAvatarDeckKind(value: unknown): value is AvatarDeckKind {
  if (!value || typeof value !== "object") return false;
  const deck = value as Record<string, unknown>;
  return (
    (deck.kind === "agent" || deck.kind === "system") &&
    typeof deck.cycle === "number" &&
    Array.isArray(deck.entries)
  );
}

export function avatarSaveErrorMessage(status: number, body: AvatarUploadErrorBody): string {
  const detail = typeof body.detail === "string" ? body.detail : `save failed: ${status}`;
  const attemptID = typeof body.attempt_id === "string" && body.attempt_id.trim()
    ? body.attempt_id.trim()
    : "";
  return attemptID ? `${detail} Reference ${attemptID}.` : detail;
}

async function fetchAvatarDecks(): Promise<AvatarDeckKind[]> {
  const res = await authedFetch("/api/admin/avatar-decks");
  if (!res.ok) throw new Error(`avatar deck fetch failed: ${res.status}`);
  const body = (await res.json()) as { decks?: unknown };
  return Array.isArray(body.decks)
    ? body.decks.filter(isAvatarDeckKind)
    : [];
}

function containedImageRect(
  stageWidth: number,
  stageHeight: number,
  naturalWidth: number,
  naturalHeight: number,
): ImageRect | null {
  if (stageWidth <= 0 || stageHeight <= 0 || naturalWidth <= 0 || naturalHeight <= 0) {
    return null;
  }
  const scale = Math.min(stageWidth / naturalWidth, stageHeight / naturalHeight);
  const width = naturalWidth * scale;
  const height = naturalHeight * scale;
  return {
    left: (stageWidth - width) / 2,
    top: (stageHeight - height) / 2,
    width,
    height,
  };
}

function imageRectContainsPoint(rect: ImageRect, point: { x: number; y: number }): boolean {
  return point.x >= 0 && point.x <= rect.width && point.y >= 0 && point.y <= rect.height;
}

export function AdminAvatarManager({ onCatalogChanged }: AdminAvatarManagerProps) {
  const [entries, setEntries] = useState<AvatarView[]>([]);
  const [listError, setListError] = useState<string | null>(null);
  const [loadingEntries, setLoadingEntries] = useState(false);
  const [decks, setDecks] = useState<AvatarDeckKind[]>([]);
  const [deckError, setDeckError] = useState<string | null>(null);
  const [loadingDeck, setLoadingDeck] = useState(false);
  const [kind, setKind] = useState<AvatarKind>("agent");
  const [name, setName] = useState("");
  const [photoFile, setPhotoFile] = useState<File | null>(null);
  const [photoURL, setPhotoURL] = useState("");
  const [imageSize, setImageSize] = useState({ width: 0, height: 0 });
  const [stageSize, setStageSize] = useState({ width: 0, height: 0 });
  const [crop, setCrop] = useState<AvatarCrop>(defaultCrop);
  const [dragging, setDragging] = useState(false);
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const stageRef = useRef<HTMLDivElement | null>(null);
  const imageRef = useRef<HTMLImageElement | null>(null);
  const avatarObjectURLsRef = useRef<string[]>([]);
  const cropDragOffsetRef = useRef<AvatarCropDragOffset>({ x: 0, y: 0 });
  const cropDragActiveRef = useRef(false);
  const pendingCropPlacementRef = useRef<PendingCropPlacement | null>(null);

  const revokeAvatarObjectURLs = useCallback(() => {
    for (const url of avatarObjectURLsRef.current) URL.revokeObjectURL(url);
    avatarObjectURLsRef.current = [];
  }, []);

  const reloadEntries = useCallback(async () => {
    setLoadingEntries(true);
    setListError(null);
    try {
      const views = await fetchAvatarViews();
      revokeAvatarObjectURLs();
      avatarObjectURLsRef.current = views
        .map((entry) => entry.avatarSrc)
        .filter((src): src is string => Boolean(src));
      setEntries(views);
    } catch (err) {
      setListError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoadingEntries(false);
    }
  }, [revokeAvatarObjectURLs]);
  const notifyCatalogChanged = useCallback(async (
    surfaceError: (message: string) => void,
    successContext: string,
  ) => {
    if (!onCatalogChanged) return;
    try {
      await onCatalogChanged();
    } catch (err) {
      const detail = err instanceof Error ? err.message : String(err);
      surfaceError(`${successContext}, but active transcript avatars did not refresh: ${detail}`);
    }
  }, [onCatalogChanged]);

  const reloadDecks = useCallback(async () => {
    setLoadingDeck(true);
    setDeckError(null);
    try {
      setDecks(await fetchAvatarDecks());
    } catch (err) {
      setDeckError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoadingDeck(false);
    }
  }, []);

  useEffect(() => {
    void reloadEntries();
    void reloadDecks();
  }, [reloadDecks, reloadEntries]);

  useEffect(() => () => revokeAvatarObjectURLs(), [revokeAvatarObjectURLs]);

  useLayoutEffect(() => {
    const node = stageRef.current;
    if (!node) return;
    const update = () => {
      setStageSize({ width: node.clientWidth, height: node.clientHeight });
    };
    update();
    const observer = new ResizeObserver(update);
    observer.observe(node);
    return () => observer.disconnect();
  }, [photoURL]);

  useEffect(() => {
    return () => {
      if (photoURL) URL.revokeObjectURL(photoURL);
    };
  }, [photoURL]);

  const imageRect = useMemo(
    () => containedImageRect(stageSize.width, stageSize.height, imageSize.width, imageSize.height),
    [imageSize.height, imageSize.width, stageSize.height, stageSize.width],
  );

  const cropStyle = useMemo(() => {
    if (!imageRect) return null;
    const clamped = clampAvatarCrop(crop, imageRect.width, imageRect.height);
    const side = clamped.size * Math.min(imageRect.width, imageRect.height);
    return {
      width: `${side}px`,
      height: `${side}px`,
      left: `${imageRect.left + clamped.center_x * imageRect.width - side / 2}px`,
      top: `${imageRect.top + clamped.center_y * imageRect.height - side / 2}px`,
    };
  }, [crop, imageRect]);

  const visibleEntries = useMemo(
    () => entries.filter((entry) => entry.kind === kind),
    [entries, kind],
  );

  const imagePointFromPointer = useCallback((event: ReactPointerEvent<HTMLDivElement>) => {
    if (!imageRect || !stageRef.current) return;
    const bounds = stageRef.current.getBoundingClientRect();
    return {
      x: event.clientX - bounds.left - imageRect.left,
      y: event.clientY - bounds.top - imageRect.top,
    };
  }, [imageRect]);

  const updateCropFromPointer = useCallback((event: ReactPointerEvent<HTMLDivElement>) => {
    if (!imageRect) return;
    const point = imagePointFromPointer(event);
    if (!point) return;
    setCrop((current) =>
      avatarCropFromImagePoint(
        current,
        imageRect.width,
        imageRect.height,
        point.x,
        point.y,
        cropDragOffsetRef.current,
      ),
    );
  }, [imagePointFromPointer, imageRect]);

  const startCropMove = useCallback((event: ReactPointerEvent<HTMLDivElement>, point: { x: number; y: number }) => {
    if (!imageRect) return;
    event.preventDefault();
    pendingCropPlacementRef.current = null;
    cropDragOffsetRef.current = avatarCropDragOffset(
      crop,
      imageRect.width,
      imageRect.height,
      point.x,
      point.y,
      cropDragHitSlopPx,
    );
    cropDragActiveRef.current = true;
    setDragging(true);
    event.currentTarget.setPointerCapture(event.pointerId);
    setCrop((current) =>
      avatarCropFromImagePoint(
        current,
        imageRect.width,
        imageRect.height,
        point.x,
        point.y,
        cropDragOffsetRef.current,
      ),
    );
  }, [crop, imageRect]);

  const startCropPointer = useCallback((event: ReactPointerEvent<HTMLDivElement>) => {
    if (!imageRect) return;
    const point = imagePointFromPointer(event);
    if (!point) return;
    if (avatarCropContainsPoint(
      crop,
      imageRect.width,
      imageRect.height,
      point.x,
      point.y,
      cropDragHitSlopPx,
    )) {
      startCropMove(event, point);
      return;
    }
    if (!imageRectContainsPoint(imageRect, point)) return;

    event.preventDefault();
    pendingCropPlacementRef.current = {
      pointerId: event.pointerId,
      x: event.clientX,
      y: event.clientY,
      moved: false,
    };
    event.currentTarget.setPointerCapture(event.pointerId);
  }, [crop, imagePointFromPointer, imageRect, startCropMove]);

  const updateCropPointer = useCallback((event: ReactPointerEvent<HTMLDivElement>) => {
    if (cropDragActiveRef.current) {
      event.preventDefault();
      updateCropFromPointer(event);
      return;
    }

    const pending = pendingCropPlacementRef.current;
    if (!pending || pending.pointerId !== event.pointerId) return;
    if (
      Math.hypot(event.clientX - pending.x, event.clientY - pending.y) >
      cropPlacementMoveThresholdPx
    ) {
      pending.moved = true;
    }
  }, [updateCropFromPointer]);

  const endCropPointer = useCallback((event: ReactPointerEvent<HTMLDivElement>) => {
    if (cropDragActiveRef.current) {
      cropDragActiveRef.current = false;
      setDragging(false);
      cropDragOffsetRef.current = { x: 0, y: 0 };
    } else {
      const pending = pendingCropPlacementRef.current;
      if (pending && pending.pointerId === event.pointerId && !pending.moved && imageRect) {
        const point = imagePointFromPointer(event);
        if (point && imageRectContainsPoint(imageRect, point)) {
          setCrop((current) =>
            avatarCropFromImagePoint(
              current,
              imageRect.width,
              imageRect.height,
              point.x,
              point.y,
            ),
          );
        }
      }
    }
    pendingCropPlacementRef.current = null;
    setDragging(false);
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId);
    }
  }, [imagePointFromPointer, imageRect]);

  const selectPhoto = useCallback((file: File | null) => {
    setFormError(null);
    setPhotoFile(file);
    setImageSize({ width: 0, height: 0 });
    setCrop(defaultCrop);
    setPhotoURL((current) => {
      if (current) URL.revokeObjectURL(current);
      return file ? URL.createObjectURL(file) : "";
    });
  }, []);

  useEffect(() => {
    const onPaste = (event: ClipboardEvent) => {
      const data = event.clipboardData;
      if (!data) return;
      const file = imageFileFromClipboard(data);
      if (!file) return;
      event.preventDefault();
      selectPhoto(file);
    };
    window.addEventListener("paste", onPaste);
    return () => window.removeEventListener("paste", onPaste);
  }, [selectPhoto]);

  async function buildAvatarBlob(): Promise<Blob> {
    const image = imageRef.current;
    if (!image || !photoFile) throw new Error("choose a source photo first");
    const canvas = document.createElement("canvas");
    canvas.width = avatarCanvasSize;
    canvas.height = avatarCanvasSize;
    const ctx = canvas.getContext("2d");
    if (!ctx) throw new Error("canvas unavailable");
    const rect = cropToSourceRect(crop, image.naturalWidth, image.naturalHeight);
    ctx.clearRect(0, 0, avatarCanvasSize, avatarCanvasSize);
    ctx.save();
    ctx.beginPath();
    ctx.arc(avatarCanvasSize / 2, avatarCanvasSize / 2, avatarCanvasSize / 2, 0, Math.PI * 2);
    ctx.clip();
    ctx.drawImage(
      image,
      rect.sx,
      rect.sy,
      rect.side,
      rect.side,
      0,
      0,
      avatarCanvasSize,
      avatarCanvasSize,
    );
    ctx.restore();
    return await new Promise((resolve, reject) => {
      canvas.toBlob((blob) => {
        if (blob) resolve(blob);
        else reject(new Error("failed to render avatar crop"));
      }, "image/png");
    });
  }

  async function saveAvatar() {
    setFormError(null);
    if (!photoFile) {
      setFormError("Choose a source photo.");
      return;
    }
    const trimmedName = name.trim();
    if (!trimmedName) {
      setFormError("Name is required.");
      return;
    }
    setSaving(true);
    try {
      const avatarBlob = await buildAvatarBlob();
      const payload = new FormData();
      payload.set("kind", kind);
      payload.set("name", trimmedName);
      payload.set("crop", JSON.stringify({
        ...clampAvatarCrop(crop, imageRef.current?.naturalWidth, imageRef.current?.naturalHeight),
        source_width: imageRef.current?.naturalWidth ?? 0,
        source_height: imageRef.current?.naturalHeight ?? 0,
      }));
      payload.set("avatar", avatarBlob, `${trimmedName}-avatar.png`);
      payload.set("backing", photoFile, photoFile.name || `${trimmedName}-backing.png`);
      const res = await authedFetch("/api/admin/avatars", {
        method: "POST",
        body: payload,
      });
      if (!res.ok) {
        const body = await res.json().catch(() => ({}));
        throw new Error(avatarSaveErrorMessage(res.status, body));
      }
      setName("");
      selectPhoto(null);
      await reloadEntries();
      await reloadDecks();
      await notifyCatalogChanged(setFormError, "Avatar saved");
    } catch (err) {
      setFormError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  }

  async function deleteAvatar(entry: AvatarView) {
    const res = await authedFetch(`/api/admin/avatars/${encodeURIComponent(entry.id)}`, {
      method: "DELETE",
    });
    if (!res.ok) {
      const body = await res.json().catch(() => ({}));
      setListError(typeof body.detail === "string" ? body.detail : `delete failed: ${res.status}`);
      return;
    }
    await reloadEntries();
    await reloadDecks();
    await notifyCatalogChanged(setListError, "Avatar deleted");
  }

  const visibleDeck = decks.find((deck) => deck.kind === kind);
  const deckEntries = visibleDeck?.entries ?? [];
  const entriesById = new Map(entries.map((entry) => [entry.id, entry]));

  return (
    <div className="admin-avatar-manager">
      <div className="admin-avatar-layout">
        <section className="admin-avatar-editor" aria-label="Add avatar">
          <div className="admin-avatar-editor-head">
            <ImagePlusIcon size={18} aria-hidden="true" />
            <h2>Add avatar</h2>
          </div>
          <div className="admin-avatar-kind" role="group" aria-label="Avatar type">
            {(["agent", "system"] as AvatarKind[]).map((option) => (
              <button
                key={option}
                type="button"
                className={kind === option ? "is-selected" : ""}
                aria-pressed={kind === option}
                onClick={() => setKind(option)}
              >
                {option}
              </button>
            ))}
          </div>
          <label className="admin-avatar-field">
            <span>Name</span>
            <input
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder={kind === "agent" ? "Agent display name" : "System display name"}
              maxLength={80}
            />
          </label>
          <label className="admin-avatar-file">
            <UploadIcon size={16} aria-hidden="true" />
            <span>{photoFile ? photoFile.name : "Choose or paste photo"}</span>
            <input
              type="file"
              accept="image/png,image/jpeg,image/webp,image/gif,image/avif,image/bmp"
              onChange={(event) => selectPhoto(event.target.files?.[0] ?? null)}
            />
          </label>

          {photoURL && (
            <>
              <div
                ref={stageRef}
                className="admin-avatar-crop-stage"
                data-dragging={dragging ? "true" : undefined}
                onPointerDown={startCropPointer}
                onPointerMove={updateCropPointer}
                onPointerUp={endCropPointer}
                onPointerCancel={endCropPointer}
              >
                <img
                  ref={imageRef}
                  src={photoURL}
                  alt=""
                  onLoad={(event) => {
                    setImageSize({
                      width: event.currentTarget.naturalWidth,
                      height: event.currentTarget.naturalHeight,
                    });
                  }}
                  draggable={false}
                  onDragStart={(event) => event.preventDefault()}
                />
                {cropStyle && <span className="admin-avatar-crop-ring" style={cropStyle} />}
              </div>
              <label className="admin-avatar-slider">
                <span>Crop size</span>
                <input
                  type="range"
                  min="0.12"
                  max="1"
                  step="0.01"
                  value={crop.size}
                  onChange={(event) =>
                    setCrop((current) =>
                      clampAvatarCrop(
                        {
                          ...current,
                          size: Number(event.target.value),
                        },
                        imageRect?.width,
                        imageRect?.height,
                      ),
                    )
                  }
                />
              </label>
            </>
          )}

          {formError && <div className="admin-avatar-error">{formError}</div>}
          <button
            type="button"
            className="btn-primary admin-avatar-save"
            disabled={saving || !photoFile}
            onClick={() => void saveAvatar()}
          >
            {saving ? <Loader2Icon size={16} className="spin" aria-hidden="true" /> : <UploadIcon size={16} aria-hidden="true" />}
            <span>{saving ? "Saving" : "Save avatar"}</span>
          </button>
        </section>

        <div className="admin-avatar-stack">
          <section className="admin-avatar-deck" aria-label="Avatar traversal">
            <div className="admin-avatar-gallery-head">
              <h2>{kind === "agent" ? "Agent traversal" : "System traversal"}</h2>
              {loadingDeck && <Loader2Icon size={16} className="spin" aria-hidden="true" />}
            </div>
            {deckError && <div className="admin-avatar-error">{deckError}</div>}
            {deckEntries.length === 0 ? (
              <p className="admin-avatar-empty">No {kind} deck yet.</p>
            ) : (
              <div className="admin-avatar-deck-list">
                {deckEntries.map((deckEntry) => {
                  const view = entriesById.get(deckEntry.avatar_id);
                  const disabled = deckEntry.used || !deckEntry.available || !view?.avatarSrc;
                  return (
                    <button
                      key={`${deckEntry.position}-${deckEntry.avatar_id}`}
                      type="button"
                      className="admin-avatar-deck-row"
                      data-used={deckEntry.used ? "true" : undefined}
                      data-available={deckEntry.available ? "true" : "false"}
                      disabled={disabled}
                      onClick={(event) => {
                        if (!view?.avatarSrc) return;
                        openAvatarPreview({
                          name: view.name,
                          avatarSrc: view.avatarSrc,
                          backingSrc: view.backing_url,
                          kind: view.kind,
                        }, event);
                      }}
                    >
                      <span className="admin-avatar-deck-position">{deckEntry.position}</span>
                      <span className="admin-avatar-card-preview" aria-hidden="true">
                        {view?.avatarSrc ? (
                          <img src={view.avatarSrc} alt="" draggable={false} />
                        ) : (
                          <ImagePlusIcon size={18} aria-hidden="true" />
                        )}
                      </span>
                      <span className="admin-avatar-deck-body">
                        <span className="admin-avatar-card-name">{deckEntry.name}</span>
                        <span className="admin-avatar-card-meta">
                          {view?.imageError
                            ? "image unavailable"
                            : deckEntry.used
                              ? `used${deckEntry.used_session_id ? ` - ${deckEntry.used_session_id}` : ""}`
                              : deckEntry.available
                                ? "remaining"
                                : "removed"}
                        </span>
                      </span>
                    </button>
                  );
                })}
              </div>
            )}
          </section>

          <section className="admin-avatar-gallery" aria-label="Avatar gallery">
            <div className="admin-avatar-gallery-head">
              <h2>{kind === "agent" ? "Agent avatars" : "System avatars"}</h2>
              {loadingEntries && <Loader2Icon size={16} className="spin" aria-hidden="true" />}
            </div>
            {listError && <div className="admin-avatar-error">{listError}</div>}
            {visibleEntries.length === 0 ? (
              <p className="admin-avatar-empty">No {kind} avatars.</p>
            ) : (
              <div className="admin-avatar-grid">
                {visibleEntries.map((entry) => (
                  <div className="admin-avatar-card" key={entry.id}>
                    <button
                      type="button"
                      className="admin-avatar-card-main"
                      aria-label={`View ${entry.name}`}
                      disabled={!entry.avatarSrc}
                      onClick={(event) =>
                        entry.avatarSrc &&
                        openAvatarPreview({
                          name: entry.name,
                          avatarSrc: entry.avatarSrc,
                          backingSrc: entry.backing_url,
                          kind: entry.kind,
                        }, event)
                      }
                    >
                      <span className="admin-avatar-card-preview" aria-hidden="true">
                        {entry.avatarSrc ? (
                          <img src={entry.avatarSrc} alt="" draggable={false} />
                        ) : (
                          <ImagePlusIcon size={18} aria-hidden="true" />
                        )}
                      </span>
                      <span className="admin-avatar-card-body">
                        <span className="admin-avatar-card-name">{entry.name}</span>
                        <span className="admin-avatar-card-meta">
                          {entry.imageError ? "image unavailable" : entry.created_by}
                        </span>
                      </span>
                    </button>
                    <button
                      type="button"
                      className="admin-avatar-delete"
                      title="delete avatar"
                      aria-label={`Delete ${entry.name}`}
                      onClick={(event) => {
                        event.stopPropagation();
                        void deleteAvatar(entry);
                      }}
                    >
                      <Trash2Icon size={15} aria-hidden="true" />
                    </button>
                  </div>
                ))}
              </div>
            )}
          </section>
        </div>
      </div>
    </div>
  );
}
