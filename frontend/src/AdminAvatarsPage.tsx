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
import { authedFetch, bootstrapAuth, logout, startLogin } from "./auth";
import {
  type AvatarCrop,
  clampAvatarCrop,
  cropToSourceRect,
} from "./adminAvatarCrop";
import { openAvatarPreview } from "./avatarPreview";

type AdminUser = NonNullable<Awaited<ReturnType<typeof bootstrapAuth>>>;

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
  avatarSrc: string;
};

type ImageRect = {
  left: number;
  top: number;
  width: number;
  height: number;
};

const defaultCrop: AvatarCrop = {
  center_x: 0.5,
  center_y: 0.5,
  size: 0.42,
};

const avatarCanvasSize = 512;

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

async function fetchAvatarViews(): Promise<AvatarView[]> {
  const res = await authedFetch("/api/avatars");
  if (!res.ok) throw new Error(`avatar list failed: ${res.status}`);
  const body = (await res.json()) as { entries?: unknown };
  const entries = Array.isArray(body.entries)
    ? body.entries.filter(isAvatarEntry)
    : [];
  const views = await Promise.all(entries.map(async (entry) => {
    const imageRes = await authedFetch(entry.avatar_url);
    if (!imageRes.ok) throw new Error(`avatar image failed: ${imageRes.status}`);
    return {
      ...entry,
      avatarSrc: URL.createObjectURL(await imageRes.blob()),
    };
  }));
  return views;
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

export function AdminAvatarsPage() {
  const [user, setUser] = useState<AdminUser | null>(null);
  const [booted, setBooted] = useState(false);
  const [authError, setAuthError] = useState<string | null>(null);
  const [entries, setEntries] = useState<AvatarView[]>([]);
  const [listError, setListError] = useState<string | null>(null);
  const [loadingEntries, setLoadingEntries] = useState(false);
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

  useEffect(() => {
    bootstrapAuth()
      .then((u) => {
        setUser(u);
        setBooted(true);
      })
      .catch((err) => {
        setAuthError(err instanceof Error ? err.message : String(err));
        setBooted(true);
      });
  }, []);

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
      avatarObjectURLsRef.current = views.map((entry) => entry.avatarSrc);
      setEntries(views);
    } catch (err) {
      setListError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoadingEntries(false);
    }
  }, [revokeAvatarObjectURLs]);

  useEffect(() => {
    if (user?.role === "admin") void reloadEntries();
  }, [reloadEntries, user?.role]);

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
    const clamped = clampAvatarCrop(crop);
    const side = clamped.size * Math.min(imageRect.width, imageRect.height);
    return {
      width: `${side}px`,
      height: `${side}px`,
      left: `${imageRect.left + clamped.center_x * imageRect.width - side / 2}px`,
      top: `${imageRect.top + clamped.center_y * imageRect.height - side / 2}px`,
    };
  }, [crop, imageRect]);

  const grouped = useMemo(() => ({
    agent: entries.filter((entry) => entry.kind === "agent"),
    system: entries.filter((entry) => entry.kind === "system"),
  }), [entries]);

  const updateCropFromPointer = useCallback((event: ReactPointerEvent<HTMLDivElement>) => {
    if (!imageRect || !stageRef.current) return;
    const bounds = stageRef.current.getBoundingClientRect();
    const x = event.clientX - bounds.left - imageRect.left;
    const y = event.clientY - bounds.top - imageRect.top;
    setCrop((current) =>
      clampAvatarCrop({
        ...current,
        center_x: x / imageRect.width,
        center_y: y / imageRect.height,
      }),
    );
  }, [imageRect]);

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
        ...clampAvatarCrop(crop),
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
        throw new Error(typeof body.detail === "string" ? body.detail : `save failed: ${res.status}`);
      }
      setName("");
      selectPhoto(null);
      await reloadEntries();
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
  }

  if (!booted) {
    return <div className="boot-state"><span className="boot-text">loading...</span></div>;
  }
  if (authError) {
    return (
      <div className="boot-state">
        <pre className="error">auth error: {authError}</pre>
        <button className="btn-secondary" type="button" onClick={() => location.reload()}>retry</button>
      </div>
    );
  }
  if (!user) {
    return (
      <div className="admin-avatar-page admin-avatar-access">
        <button type="button" className="btn-primary" onClick={startLogin}>Sign in</button>
      </div>
    );
  }
  if (user.role !== "admin") {
    return (
      <div className="admin-avatar-page admin-avatar-access">
        <h1>Avatar admin</h1>
        <p>Admin access required.</p>
        <button type="button" className="btn-secondary" onClick={logout}>Sign out</button>
      </div>
    );
  }

  return (
    <div className="admin-avatar-page">
      <header className="admin-avatar-header">
        <div>
          <h1>Avatar admin</h1>
          <p>{user.email}</p>
        </div>
        <a className="btn-secondary admin-avatar-home" href="/">Back to app</a>
      </header>

      <main className="admin-avatar-layout">
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
            <span>{photoFile ? photoFile.name : "Choose photo"}</span>
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
                onPointerDown={(event) => {
                  setDragging(true);
                  event.currentTarget.setPointerCapture(event.pointerId);
                  updateCropFromPointer(event);
                }}
                onPointerMove={(event) => {
                  if (dragging) updateCropFromPointer(event);
                }}
                onPointerUp={(event) => {
                  setDragging(false);
                  event.currentTarget.releasePointerCapture(event.pointerId);
                }}
                onPointerCancel={() => setDragging(false)}
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
                    setCrop((current) => clampAvatarCrop({
                      ...current,
                      size: Number(event.target.value),
                    }))
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

        <section className="admin-avatar-gallery" aria-label="Avatar gallery">
          <div className="admin-avatar-gallery-head">
            <h2>Avatars</h2>
            {loadingEntries && <Loader2Icon size={16} className="spin" aria-hidden="true" />}
          </div>
          {listError && <div className="admin-avatar-error">{listError}</div>}
          {(["agent", "system"] as AvatarKind[]).map((group) => (
            <div className="admin-avatar-group" key={group}>
              <h3>{group}</h3>
              {grouped[group].length === 0 ? (
                <p className="admin-avatar-empty">No {group} avatars.</p>
              ) : (
                <div className="admin-avatar-grid">
                  {grouped[group].map((entry) => (
                    <div className="admin-avatar-card" key={entry.id}>
                      <button
                        type="button"
                        className="admin-avatar-card-main"
                        aria-label={`View ${entry.name}`}
                        onClick={(event) =>
                          openAvatarPreview({
                            name: entry.name,
                            avatarSrc: entry.avatarSrc,
                            backingSrc: entry.backing_url,
                            kind: entry.kind,
                          }, event)
                        }
                      >
                        <span className="admin-avatar-card-preview" aria-hidden="true">
                          <img src={entry.avatarSrc} alt="" draggable={false} />
                        </span>
                        <span className="admin-avatar-card-body">
                          <span className="admin-avatar-card-name">{entry.name}</span>
                          <span className="admin-avatar-card-meta">{entry.created_by}</span>
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
            </div>
          ))}
        </section>
      </main>
    </div>
  );
}
