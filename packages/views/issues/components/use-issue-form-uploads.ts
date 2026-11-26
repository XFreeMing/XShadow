"use client";

import {
  useCallback,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type Dispatch,
  type RefObject,
  type SetStateAction,
} from "react";
import { attachmentToDraftUpload, type DraftUpload } from "@multica/core/drafts";
import type { UploadGate } from "../../editor/use-upload-gate";
import type { ContentEditorRef } from "../../editor/content-editor";
import {
  useCoordinatedUploads,
  type CoordinatedUploads,
  type UploadDraftBinding,
} from "../../editor/use-coordinated-uploads";

function appendMarkdown(existing: string, markdown: string): string {
  return existing.trim() ? `${existing.replace(/\s+$/, "")}\n\n${markdown}` : markdown;
}

export function useIssueFormUploads(
  formUploadKey: string,
  editorGate: UploadGate,
  editorRef: RefObject<ContentEditorRef | null>,
  description: string,
  setDescription: Dispatch<SetStateAction<string>>,
): CoordinatedUploads & { clearUploads: () => void } {
  const instanceId = useId();
  const [attachments, setAttachments] = useState<DraftUpload[]>([]);
  const attachmentsRef = useRef(attachments);
  const updateAttachments = useCallback((update: (current: DraftUpload[]) => DraftUpload[]) => {
    const next = update(attachmentsRef.current);
    attachmentsRef.current = next;
    setAttachments(next);
  }, []);
  const clearUploads = useCallback(() => updateAttachments(() => []), [updateAttachments]);
  const descriptionRef = useRef(description);
  descriptionRef.current = description;

  useLayoutEffect(() => () => {
    attachmentsRef.current = [];
  }, []);

  const binding = useMemo<UploadDraftBinding>(
    () => ({
      registryKey: `issue-form:${formUploadKey}:${instanceId}`,
      getUploads: () => attachmentsRef.current,
      addUpload: (u) => {
        updateAttachments((cur) =>
          cur.some((x) => x.clientUploadId === u.clientUploadId) ? cur : [...cur, u],
        );
      },
      settleUpload: (id, att) => {
        updateAttachments((cur) => {
          if (!cur.some((u) => u.clientUploadId === id)) return cur;
          return cur.map((u) =>
            u.clientUploadId === id ? { ...attachmentToDraftUpload(att), clientUploadId: id } : u,
          );
        });
      },
      failUpload: (id, error) => {
        updateAttachments((cur) => {
          const target = cur.find((u) => u.clientUploadId === id);
          if (!target) return cur;
          return cur.map((u) =>
            u.clientUploadId === id
              ? {
                  clientUploadId: id,
                  status: "failed",
                  filename: target.filename,
                  size: target.size,
                  contentType: target.contentType,
                  error,
                }
              : u,
          );
        });
      },
      removeUpload: (id) => {
        updateAttachments((cur) => cur.filter((u) => u.clientUploadId !== id));
      },
      getBody: () => descriptionRef.current,
      appendToBody: (md) =>
        setDescription((current) => appendMarkdown(current, md)),
    }),
    [formUploadKey, instanceId, setDescription, updateAttachments],
  );

  const coordinated = useCoordinatedUploads(binding, attachments, {}, editorGate, editorRef);
  return { ...coordinated, clearUploads };
}
