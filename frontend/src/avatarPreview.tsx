import { useEffect, useState } from "react";
import { XIcon } from "lucide-react";
import { authedFetch } from "./auth";

export type AvatarPreviewDetail = {
  name: string;
  avatarSrc: string;
  backingSrc?: string;
  kind?: string;
};

const AVATAR_PREVIEW_EVENT = "tank-avatar-preview";
const AVATAR_PREVIEW_EDIT_AVAILABILITY_EVENT =
  "tank-avatar-preview-edit-availability";
const AVATAR_PREVIEW_EDIT_REQUEST_EVENT = "tank-avatar-preview-edit-request";

let avatarPreviewEditAvailable = false;

type AvatarPreviewEditAvailabilityDetail = {
  available: boolean;
};

export function openAvatarPreview(
  detail: AvatarPreviewDetail,
  event?: { stopPropagation?: () => void; preventDefault?: () => void },
) {
  event?.stopPropagation?.();
  event?.preventDefault?.();
  if (typeof window === "undefined") return;
  window.dispatchEvent(new CustomEvent<AvatarPreviewDetail>(AVATAR_PREVIEW_EVENT, { detail }));
}

export function avatarPreviewIsEditable(detail: AvatarPreviewDetail): boolean {
  return detail.kind === "agent" || detail.kind === "system";
}

export function setAvatarPreviewEditAvailable(available: boolean) {
  avatarPreviewEditAvailable = available;
  if (typeof window === "undefined") return;
  window.dispatchEvent(
    new CustomEvent<AvatarPreviewEditAvailabilityDetail>(
      AVATAR_PREVIEW_EDIT_AVAILABILITY_EVENT,
      { detail: { available } },
    ),
  );
}

export function addAvatarPreviewEditRequestListener(
  listener: (detail: AvatarPreviewDetail) => void,
): () => void {
  if (typeof window === "undefined") return () => undefined;
  const onEditRequest = (event: Event) => {
    const detail = (event as CustomEvent<AvatarPreviewDetail>).detail;
    if (!detail) return;
    listener(detail);
  };
  window.addEventListener(AVATAR_PREVIEW_EDIT_REQUEST_EVENT, onEditRequest);
  return () =>
    window.removeEventListener(
      AVATAR_PREVIEW_EDIT_REQUEST_EVENT,
      onEditRequest,
    );
}

function requestAvatarPreviewEdit(detail: AvatarPreviewDetail) {
  if (typeof window === "undefined") return;
  window.dispatchEvent(
    new CustomEvent<AvatarPreviewDetail>(AVATAR_PREVIEW_EDIT_REQUEST_EVENT, {
      detail,
    }),
  );
}

function displayLabel(kind?: string): string {
  switch (kind) {
    case "agent":
      return "Agent avatar";
    case "system":
      return "System avatar";
    case "personal":
      return "Profile avatar";
    default:
      return "Avatar";
  }
}

async function resolvePreviewSource(detail: AvatarPreviewDetail): Promise<{
  src: string;
  revoke: boolean;
}> {
  const source = detail.backingSrc || detail.avatarSrc;
  if (source.startsWith("/api/")) {
    const res = await authedFetch(source);
    if (!res.ok) throw new Error(`image fetch failed: ${res.status}`);
    return { src: URL.createObjectURL(await res.blob()), revoke: true };
  }
  return { src: source, revoke: false };
}

export function AvatarPreviewHost() {
  const [preview, setPreview] = useState<AvatarPreviewDetail | null>(null);
  const [resolvedSrc, setResolvedSrc] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [editAvailable, setEditAvailable] = useState(
    avatarPreviewEditAvailable,
  );

  useEffect(() => {
    const onPreview = (event: Event) => {
      const detail = (event as CustomEvent<AvatarPreviewDetail>).detail;
      if (!detail?.avatarSrc) return;
      setPreview(detail);
    };
    window.addEventListener(AVATAR_PREVIEW_EVENT, onPreview);
    return () => window.removeEventListener(AVATAR_PREVIEW_EVENT, onPreview);
  }, []);

  useEffect(() => {
    const onEditAvailability = (event: Event) => {
      const detail = (
        event as CustomEvent<AvatarPreviewEditAvailabilityDetail>
      ).detail;
      setEditAvailable(detail?.available === true);
    };
    window.addEventListener(
      AVATAR_PREVIEW_EDIT_AVAILABILITY_EVENT,
      onEditAvailability,
    );
    return () =>
      window.removeEventListener(
        AVATAR_PREVIEW_EDIT_AVAILABILITY_EVENT,
        onEditAvailability,
      );
  }, []);

  useEffect(() => {
    if (!preview) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") setPreview(null);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [preview]);

  useEffect(() => {
    if (!preview) {
      setResolvedSrc("");
      setError(null);
      return;
    }
    let cancelled = false;
    let revokeURL = "";
    setError(null);
    setResolvedSrc("");
    void resolvePreviewSource(preview)
      .then(({ src, revoke }) => {
        if (cancelled) {
          if (revoke) URL.revokeObjectURL(src);
          return;
        }
        revokeURL = revoke ? src : "";
        setResolvedSrc(src);
      })
      .catch((err) => {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err));
      });
    return () => {
      cancelled = true;
      if (revokeURL) URL.revokeObjectURL(revokeURL);
    };
  }, [preview]);

  if (!preview) return null;
  const canEdit = editAvailable && avatarPreviewIsEditable(preview);

  return (
    <div className="avatar-lightbox" role="dialog" aria-modal="true" aria-label={preview.name}>
      <button
        type="button"
        className="avatar-lightbox-backdrop"
        aria-label="Close avatar preview"
        onClick={() => setPreview(null)}
      />
      <div className="avatar-lightbox-panel">
        <button
          type="button"
          className="avatar-lightbox-close"
          aria-label="Close avatar preview"
          title="close"
          onClick={() => setPreview(null)}
        >
          <XIcon size={18} aria-hidden="true" />
        </button>
        <div className="avatar-lightbox-content" data-has-backing={preview.backingSrc ? "true" : "false"}>
          <figure className="avatar-lightbox-figure avatar-lightbox-figure-full">
            <div className="avatar-lightbox-media">
              {resolvedSrc ? (
                <img src={resolvedSrc} alt={preview.name} />
              ) : error ? (
                <div className="avatar-lightbox-error">{error}</div>
              ) : (
                <div className="avatar-lightbox-loading">loading...</div>
              )}
            </div>
            <figcaption>{preview.backingSrc ? "Full image" : "Image"}</figcaption>
          </figure>
          {preview.backingSrc && (
            <figure className="avatar-lightbox-figure avatar-lightbox-figure-icon">
              <div className="avatar-lightbox-icon-frame">
                <img src={preview.avatarSrc} alt={`${preview.name} icon`} draggable={false} />
              </div>
              <figcaption>Avatar icon</figcaption>
            </figure>
          )}
        </div>
        <div className="avatar-lightbox-caption">
          <span className="avatar-lightbox-caption-title">{preview.name}</span>
          <span className="avatar-lightbox-caption-actions">
            <span>{displayLabel(preview.kind)}</span>
            {canEdit && (
              <button
                type="button"
                className="avatar-lightbox-edit"
                onClick={() => {
                  const detail = preview;
                  setPreview(null);
                  requestAvatarPreviewEdit(detail);
                }}
              >
                Edit
              </button>
            )}
          </span>
        </div>
      </div>
    </div>
  );
}
