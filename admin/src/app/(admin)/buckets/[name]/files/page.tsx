"use client";

import Link from "next/link";
import { use, useCallback, useEffect, useMemo, useState } from "react";
import {
  Folder,
  File,
  FileJson,
  FileText,
  FileCode,
  FileImage,
  FileVideo,
  FileAudio,
  FileArchive,
  Loader2,
  Info,
  Eye,
  Download,
  Trash2,
} from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogClose,
} from "@/components/ui/dialog";

interface FileRow {
  id: string;
  filename: string;
  folder_path: string;
  content_type: unknown;
  size_bytes: number;
  uploaded_at: string;
  expires_at: string | null;
}

function safeContentType(ct: unknown): string | null {
  if (!ct || typeof ct !== "string") return null;
  return ct;
}

interface FileListResponse {
  files: FileRow[];
  total: number;
  offset: number;
  limit: number;
}

interface ExplorerItem {
  kind: "folder" | "file";
  path: string;
  name: string;
  fileCount?: number;
  file?: FileRow;
}

interface MetadataModalState {
  isOpen: boolean;
  item?: ExplorerItem;
  folderSize?: number;
  folderLatestFile?: { filename: string; date: string };
}

function getFileIcon(contentType: unknown, className: string = "w-4 h-4") {
  if (!contentType || typeof contentType !== "string") return <File className={className} />;
  const ct = contentType.toLowerCase();

  if (ct.includes("json")) return <FileJson className={className} />;
  if (ct.includes("text")) return <FileText className={className} />;
  if (ct.includes("code") || ct.includes("javascript") || ct.includes("typescript") || ct.includes("python") || ct.includes("java")) {
    return <FileCode className={className} />;
  }
  if (ct.includes("image")) return <FileImage className={className} />;
  if (ct.includes("video")) return <FileVideo className={className} />;
  if (ct.includes("audio")) return <FileAudio className={className} />;
  if (ct.includes("zip") || ct.includes("rar") || ct.includes("tar") || ct.includes("gzip")) {
    return <FileArchive className={className} />;
  }

  return <File className={className} />;
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(2)} MB`;
  return `${(bytes / 1024 / 1024 / 1024).toFixed(2)} GB`;
}

function normalizeFolderPathInput(input: string): string {
  const trimmed = input.trim().replace(/\\/g, "/");
  if (!trimmed) return "";

  const parts = trimmed
    .split("/")
    .map((part) => part.trim())
    .filter(Boolean);

  if (parts.some((part) => part === "." || part === "..")) return "";

  return parts.join("/");
}

export default function BucketFilesPage({
  params,
}: {
  params: Promise<{ name: string }>;
}) {
  const { name } = use(params);

  const [rows, setRows] = useState<FileRow[]>([]);
  const [currentFolder, setCurrentFolder] = useState("");
  const [createFolderInput, setCreateFolderInput] = useState("");
  const [customFolders, setCustomFolders] = useState<string[]>([]);
  const [fileInput, setFileInput] = useState<File | null>(null);
  const [uploading, setUploading] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [metadataModal, setMetadataModal] = useState<MetadataModalState>({
    isOpen: false,
  });
  const [deleteConfirmation, setDeleteConfirmation] = useState<{
    isOpen: boolean;
    fileId?: string;
    fileName?: string;
    isDeleting?: boolean;
  }>({
    isOpen: false,
  });

  const localFolderStorageKey = useMemo(
    () => `mesahub:bucket-folders:${name}`,
    [name]
  );

  useEffect(() => {
    try {
      const raw = window.localStorage.getItem(localFolderStorageKey);
      if (!raw) return;
      const parsed = JSON.parse(raw) as unknown;
      if (!Array.isArray(parsed)) return;

      const normalized = Array.from(
        new Set(
          parsed
            .filter((value): value is string => typeof value === "string")
            .map((value) => normalizeFolderPathInput(value))
            .filter(Boolean)
        )
      ).sort();

      setCustomFolders(normalized);
    } catch {
      // Ignore bad local state
    }
  }, [localFolderStorageKey]);

  useEffect(() => {
    try {
      window.localStorage.setItem(localFolderStorageKey, JSON.stringify(customFolders));
    } catch {
      // Ignore write failures
    }
  }, [customFolders, localFolderStorageKey]);

  const loadFiles = useCallback(async () => {
    setLoading(true);
    setError("");

    const query = new URLSearchParams({
      limit: "200",
      offset: "0",
      sort: "uploaded_at",
      order: "desc",
    });

    const trimmedFilter = currentFolder.trim();
    if (trimmedFilter) query.set("folder_prefix", trimmedFilter);

    const res = await fetch(`/api/buckets/${encodeURIComponent(name)}/files?${query.toString()}`, {
      cache: "no-store",
    });
    const data = (await res.json().catch(() => null)) as
      | (FileListResponse & { error?: string })
      | null;

    setLoading(false);

    if (!res.ok || !data) {
      setError(data?.error ?? "Failed to load files");
      return;
    }

    setRows(Array.isArray(data.files) ? data.files : []);
  }, [name, currentFolder]);

  useEffect(() => {
    loadFiles().catch((err: unknown) => {
      setLoading(false);
      setError((err as Error)?.message ?? "Failed to load files");
    });
  }, [loadFiles]);

  const folderOptions = useMemo(() => {
    const set = new Set<string>();
    for (const row of rows) {
      if (row.folder_path) set.add(row.folder_path);
    }
    for (const folder of customFolders) {
      set.add(folder);
    }
    return Array.from(set).sort();
  }, [rows, customFolders]);

  function onSelectFolder(path: string) {
    setCurrentFolder(path);
    setError("");
  }

  const breadcrumbSegments = useMemo(() => {
    if (!currentFolder) return [];
    return currentFolder.split("/").filter(Boolean);
  }, [currentFolder]);

  const breadcrumbItems = useMemo(() => {
    const items: { label: string; path: string }[] = [];
    let acc = "";
    for (const segment of breadcrumbSegments) {
      acc = acc ? `${acc}/${segment}` : segment;
      items.push({ label: segment, path: acc });
    }
    return items;
  }, [breadcrumbSegments]);

  const childFolders = useMemo(() => {
    const prefix = currentFolder ? `${currentFolder}/` : "";
    const seen = new Map<string, number>();

    for (const folderPath of folderOptions) {
      if (currentFolder) {
        if (!folderPath.startsWith(prefix)) continue;
      }

      const remainder = currentFolder
        ? folderPath.slice(prefix.length)
        : folderPath;
      if (!remainder) continue;

      const next = remainder.split("/")[0];
      if (!next) continue;

      const childPath = currentFolder ? `${currentFolder}/${next}` : next;
      if (!seen.has(childPath)) seen.set(childPath, 0);
    }

    for (const row of rows) {
      if (row.folder_path === currentFolder) continue;

      if (currentFolder) {
        if (!row.folder_path.startsWith(prefix)) continue;
      } else if (!row.folder_path) {
        continue;
      }

      const remainder = currentFolder
        ? row.folder_path.slice(prefix.length)
        : row.folder_path;
      if (!remainder) continue;

      const next = remainder.split("/")[0];
      if (!next) continue;
      const childPath = currentFolder ? `${currentFolder}/${next}` : next;
      seen.set(childPath, (seen.get(childPath) ?? 0) + 1);
    }

    return Array.from(seen.entries())
      .map(([path, count]) => ({ path, count }))
      .sort((a, b) => a.path.localeCompare(b.path));
  }, [currentFolder, folderOptions, rows]);

  const filesInCurrentFolder = useMemo(() => {
    return rows
      .filter((row) => row.folder_path === currentFolder)
      .sort((a, b) => Date.parse(b.uploaded_at) - Date.parse(a.uploaded_at));
  }, [rows, currentFolder]);

  const explorerItems = useMemo<ExplorerItem[]>(() => {
    const folders: ExplorerItem[] = childFolders.map((folder) => ({
      kind: "folder",
      path: folder.path,
      name: folder.path.split("/").pop() || folder.path,
      fileCount: folder.count,
    }));

    const files: ExplorerItem[] = filesInCurrentFolder.map((file) => ({
      kind: "file",
      path: `${file.folder_path}/${file.filename}`,
      name: file.filename,
      file,
    }));

    return [...folders, ...files];
  }, [childFolders, filesInCurrentFolder]);

  const folderStatistics = useMemo(
    () => {
      const stats: Record<string, { size: number; latestFile: { filename: string; date: string } | null }> = {};

      for (const folderPath of childFolders.map((f) => f.path)) {
        let totalSize = 0;
        let latestFile: { filename: string; date: string } | null = null;
        let latestDate = 0;

        for (const file of rows) {
          if (file.folder_path === folderPath || file.folder_path.startsWith(`${folderPath}/`)) {
            totalSize += file.size_bytes;
            const uploadedTime = Date.parse(file.uploaded_at);
            if (uploadedTime > latestDate) {
              latestDate = uploadedTime;
              latestFile = { filename: file.filename, date: file.uploaded_at };
            }
          }
        }

        stats[folderPath] = { size: totalSize, latestFile };
      }

      return stats;
    },
    [childFolders, rows]
  );

  function openMetadataModal(item: ExplorerItem) {
    if (item.kind === "folder") {
      const stats = folderStatistics[item.path];
      setMetadataModal({
        isOpen: true,
        item,
        folderSize: stats?.size ?? 0,
        folderLatestFile: stats?.latestFile ?? undefined,
      });
    } else {
      setMetadataModal({
        isOpen: true,
        item,
      });
    }
  }

  function onCreateFolder() {
    const raw = createFolderInput.trim();
    const isAbsolute = raw.startsWith("/");
    const candidate = isAbsolute
      ? raw.slice(1)
      : currentFolder
        ? `${currentFolder}/${raw}`
        : raw;
    const normalized = normalizeFolderPathInput(candidate);
    if (!normalized) {
      setError("Enter a valid folder path (e.g. docs/api or /docs/api)");
      return;
    }

    setError("");
    setCustomFolders((prev) => {
      if (prev.includes(normalized)) return prev;
      return [...prev, normalized].sort();
    });

    onSelectFolder(normalized);
    setCreateFolderInput("");
  }

  async function onUpload() {
    if (!fileInput) {
      setError("Please choose a file to upload");
      return;
    }

    setUploading(true);
    setError("");

    const formData = new FormData();
    formData.append("file", fileInput);
    formData.append("filename", fileInput.name);

    const trimmedFolder = currentFolder.trim();
    if (trimmedFolder) {
      formData.append("folder_path", trimmedFolder);
    }

    const res = await fetch(`/api/buckets/${encodeURIComponent(name)}/files`, {
      method: "POST",
      body: formData,
    });

    const data = (await res.json().catch(() => null)) as { error?: string } | null;
    setUploading(false);

    if (!res.ok) {
      setError(data?.error ?? "Upload failed");
      return;
    }

    setFileInput(null);
    const fileInputElement = document.getElementById("file-upload-input") as HTMLInputElement | null;
    if (fileInputElement) fileInputElement.value = "";

    await loadFiles();
  }

  async function onDelete(id: string) {
    const file = rows.find((r) => r.id === id);
    setDeleteConfirmation({
      isOpen: true,
      fileId: id,
      fileName: file?.filename,
      isDeleting: false,
    });
  }

  async function confirmDelete() {
    if (!deleteConfirmation.fileId) return;

    setDeleteConfirmation((prev) => ({ ...prev, isDeleting: true }));
    setError("");

    const res = await fetch(
      `/api/buckets/${encodeURIComponent(name)}/files/${encodeURIComponent(deleteConfirmation.fileId)}`,
      { method: "DELETE" }
    );

    setDeleteConfirmation({ isOpen: false });

    if (!res.ok) {
      const data = (await res.json().catch(() => null)) as { error?: string } | null;
      setError(data?.error ?? "Failed to delete file");
      return;
    }

    await loadFiles();
  }

  return (
    <div className="px-6 py-8 max-w-6xl mx-auto w-full">
      <div className="mb-6">
        <Link href="/buckets" className="text-sm text-neutral-400 hover:text-white">
          Back to buckets
        </Link>
        <h1 className="text-xl font-semibold mt-3">Bucket files - <span className="font-mono">{name}</span></h1>
        <p className="text-sm text-neutral-400 mt-1">
          Browse folders and files in one list, then upload or create folders from the current location.
        </p>
      </div>

      {error && <p className="text-sm text-red-400 mb-4">{error}</p>}

      <div className="border border-neutral-800 rounded-lg overflow-hidden">
        <div className="px-4 py-3 border-b border-neutral-800 text-sm text-neutral-300 flex items-center flex-wrap gap-2">
          <button type="button" onClick={() => onSelectFolder("")} className="hover:text-white">
            /
          </button>
          {breadcrumbItems.map((item) => (
            <span key={item.path} className="flex items-center gap-2">
              <span className="text-neutral-600">/</span>
              <button type="button" onClick={() => onSelectFolder(item.path)} className="hover:text-white font-mono">
                {item.label}
              </button>
            </span>
          ))}
        </div>

        <div className="px-4 py-3 border-b border-neutral-800 bg-neutral-950/40">
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
            <div className="flex items-center gap-2">
              <input
                id="file-upload-input"
                type="file"
                onChange={(e) => setFileInput(e.target.files?.[0] ?? null)}
                className="flex-1 text-sm text-neutral-300 file:bg-neutral-800 file:border-0 file:text-neutral-200 file:px-3 file:py-1.5 file:rounded file:mr-3"
              />
              <button
                type="button"
                onClick={() => onUpload().catch((err: unknown) => setError((err as Error)?.message ?? "Upload failed"))}
                disabled={uploading}
                className="text-sm bg-white text-black px-3 py-1.5 rounded font-medium hover:bg-neutral-200 transition-colors disabled:opacity-60"
              >
                {uploading ? "Uploading..." : "Upload"}
              </button>
            </div>

            <div className="flex items-center gap-2">
              <input
                type="text"
                value={createFolderInput}
                onChange={(e) => setCreateFolderInput(e.target.value)}
                placeholder={`Create folder in ${currentFolder || "/"} (use /path for absolute)`}
                className="flex-1 text-sm bg-neutral-900 border border-neutral-700 rounded px-3 py-2 text-white placeholder:text-neutral-600 outline-none focus:border-neutral-500"
              />
              <button
                type="button"
                onClick={onCreateFolder}
                className="text-sm border border-neutral-700 text-neutral-200 px-3 py-1.5 rounded hover:border-neutral-500"
              >
                New folder
              </button>
            </div>
          </div>
          <p className="text-xs text-neutral-500 mt-2">
            Current folder: <span className="font-mono">{currentFolder || "/"}</span>
          </p>
        </div>

        <div className="px-4 py-3 border-b border-neutral-800 text-sm text-neutral-400">
          {loading
            ? "Loading..."
            : `${explorerItems.length} item${explorerItems.length === 1 ? "" : "s"} in this folder (${childFolders.length} folder${childFolders.length === 1 ? "" : "s"}, ${filesInCurrentFolder.length} file${filesInCurrentFolder.length === 1 ? "" : "s"})`}
        </div>

        {explorerItems.length === 0 && !loading ? (
          <div className="px-4 py-10 text-sm text-neutral-500">This folder is empty.</div>
        ) : (
          <table className="w-full text-sm">
            <thead className="bg-neutral-900/60 text-neutral-400 text-xs uppercase">
              <tr>
                <th className="text-left px-4 py-3">Name</th>
                <th className="text-left px-4 py-3">Type</th>
                <th className="text-left px-4 py-3">Size</th>
                <th className="text-left px-4 py-3">Updated</th>
                <th className="text-right px-4 py-3">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-neutral-800">
              {currentFolder ? (
                <tr className="hover:bg-neutral-800/40 transition-colors">
                  <td className="px-4 py-3 text-neutral-200 font-mono flex items-center gap-2">
                    <Folder className="w-4 h-4 text-neutral-400" />
                    <button
                      type="button"
                      onClick={() => {
                        const parts = currentFolder.split("/").filter(Boolean);
                        parts.pop();
                        onSelectFolder(parts.join("/"));
                      }}
                      className="hover:text-white"
                    >
                      ..
                    </button>
                  </td>
                  <td className="px-4 py-3 text-neutral-400">Folder</td>
                  <td className="px-4 py-3 text-neutral-500">-</td>
                  <td className="px-4 py-3 text-neutral-500">-</td>
                  <td className="px-4 py-3 text-right">
                    <span className="text-xs text-neutral-500">Open parent</span>
                  </td>
                </tr>
              ) : null}

              {explorerItems.map((item) => {
                if (item.kind === "folder") {
                  const stats = folderStatistics[item.path];
                  return (
                    <tr key={`folder:${item.path}`} className="hover:bg-neutral-800/40 transition-colors cursor-pointer" onClick={() => onSelectFolder(item.path)}>
                      <td className="px-4 py-3 text-neutral-200 font-mono flex items-center gap-2">
                        <Folder className="w-4 h-4 text-neutral-400" />
                        <span className="hover:text-white">
                          {item.name}
                        </span>
                      </td>
                      <td className="px-4 py-3 text-neutral-400">Folder</td>
                      <td className="px-4 py-3 text-neutral-500">{formatBytes(stats?.size ?? 0)}</td>
                      <td className="px-4 py-3 text-neutral-500">
                        {stats?.latestFile ? new Date(stats.latestFile.date).toLocaleString() : "-"}
                      </td>
                      <td className="px-4 py-3 text-right flex justify-end gap-2">
                        <button
                          type="button"
                          onClick={(e) => {
                            e.stopPropagation();
                            openMetadataModal(item);
                          }}
                          className="text-xs text-neutral-400 hover:text-white"
                        >
                          <Info className="w-4 h-4" />
                        </button>
                        <span className="text-xs text-neutral-500">
                          {item.fileCount ?? 0} file{(item.fileCount ?? 0) === 1 ? "" : "s"}
                        </span>
                      </td>
                    </tr>
                  );
                }

                const row = item.file!;
                return (
                  <tr key={row.id} className="hover:bg-neutral-800/40 transition-colors">
                    <td className="px-4 py-3 text-neutral-200 flex items-center gap-2">
                      {getFileIcon(row.content_type, "w-4 h-4 text-neutral-400")}
                      {row.filename}
                    </td>
                    <td className="px-4 py-3 text-neutral-400">{safeContentType(row.content_type) ?? "File"}</td>
                    <td className="px-4 py-3 text-neutral-400">{formatBytes(row.size_bytes)}</td>
                    <td className="px-4 py-3 text-neutral-400">{new Date(row.uploaded_at).toLocaleString()}</td>
                    <td className="px-4 py-3">
                      <div className="flex items-center justify-end gap-3">
                        <a
                          href={`/api/buckets/${encodeURIComponent(name)}/files/${encodeURIComponent(row.id)}`}
                          target="_blank"
                          rel="noreferrer"
                          className="text-neutral-400 hover:text-white transition-colors"
                          title="View"
                        >
                          <Eye className="w-4 h-4" />
                        </a>
                        <a
                          href={`/api/buckets/${encodeURIComponent(name)}/files/${encodeURIComponent(row.id)}`}
                          download={row.filename}
                          className="text-blue-400 hover:text-blue-300 transition-colors"
                          title="Download"
                        >
                          <Download className="w-4 h-4" />
                        </a>
                        <button
                          type="button"
                          onClick={() => onDelete(row.id)}
                          className="text-red-400 hover:text-red-300 transition-colors"
                          title="Delete"
                        >
                          <Trash2 className="w-4 h-4" />
                        </button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>

      <Dialog open={metadataModal.isOpen} onOpenChange={(open) => setMetadataModal({ ...metadataModal, isOpen: open })}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              {metadataModal.item?.kind === "folder" ? (
                <Folder className="w-4 h-4" />
              ) : (
                getFileIcon(metadataModal.item?.file?.content_type ?? null, "w-4 h-4")
              )}
              {metadataModal.item?.name}
            </DialogTitle>
            <DialogClose />
          </DialogHeader>

          <div className="space-y-4">
            {metadataModal.item?.kind === "folder" ? (
              <>
                <div>
                  <p className="text-xs text-neutral-500 uppercase tracking-wide">Full Path</p>
                  <p className="font-mono text-sm text-neutral-300 break-all">
                    /{metadataModal.item.path}
                  </p>
                </div>

                <div className="grid grid-cols-2 gap-4">
                  <div>
                    <p className="text-xs text-neutral-500 uppercase tracking-wide">Total Size</p>
                    <p className="font-mono text-sm text-white">
                      {formatBytes(metadataModal.folderSize ?? 0)}
                    </p>
                  </div>
                  <div>
                    <p className="text-xs text-neutral-500 uppercase tracking-wide">Files</p>
                    <p className="font-mono text-sm text-white">
                      {metadataModal.item.fileCount ?? 0}
                    </p>
                  </div>
                </div>

                {metadataModal.folderLatestFile && (
                  <div>
                    <p className="text-xs text-neutral-500 uppercase tracking-wide">Latest File</p>
                    <div className="bg-neutral-900 rounded px-3 py-2">
                      <p className="font-mono text-xs text-neutral-300 break-all">
                        {metadataModal.folderLatestFile.filename}
                      </p>
                      <p className="text-xs text-neutral-500 mt-1">
                        {new Date(metadataModal.folderLatestFile.date).toLocaleString()}
                      </p>
                    </div>
                  </div>
                )}
              </>
            ) : (
              <>
                <div>
                  <p className="text-xs text-neutral-500 uppercase tracking-wide">Full Path</p>
                  <p className="font-mono text-sm text-neutral-300 break-all">
                    {metadataModal.item?.file?.folder_path && `/${metadataModal.item.file.folder_path}/`}
                    {metadataModal.item?.file?.filename}
                  </p>
                </div>

                <div className="grid grid-cols-2 gap-4">
                  <div>
                    <p className="text-xs text-neutral-500 uppercase tracking-wide">Size</p>
                    <p className="font-mono text-sm text-white">
                      {formatBytes(metadataModal.item?.file?.size_bytes ?? 0)}
                    </p>
                  </div>
                  <div>
                    <p className="text-xs text-neutral-500 uppercase tracking-wide">Type</p>
                    <p className="font-mono text-sm text-white">
                      {safeContentType(metadataModal.item?.file?.content_type) ?? "Unknown"}
                    </p>
                  </div>
                </div>

                <div>
                  <p className="text-xs text-neutral-500 uppercase tracking-wide">Uploaded</p>
                  <p className="text-sm text-neutral-300">
                    {metadataModal.item?.file?.uploaded_at
                      ? new Date(metadataModal.item.file.uploaded_at).toLocaleString()
                      : "-"}
                  </p>
                </div>

                {metadataModal.item?.file?.expires_at && (
                  <div>
                    <p className="text-xs text-neutral-500 uppercase tracking-wide">Expires</p>
                    <p className="text-sm text-neutral-300">
                      {new Date(metadataModal.item.file.expires_at).toLocaleString()}
                    </p>
                  </div>
                )}

                <div>
                  <p className="text-xs text-neutral-500 uppercase tracking-wide">File ID</p>
                  <p className="font-mono text-xs text-neutral-400 break-all">
                    {metadataModal.item?.file?.id}
                  </p>
                </div>
              </>
            )}
          </div>
        </DialogContent>
      </Dialog>

      <Dialog
        open={deleteConfirmation.isOpen}
        onOpenChange={(open) =>
          setDeleteConfirmation((prev) => ({ ...prev, isOpen: open }))
        }
      >
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Trash2 className="w-5 h-5 text-red-400" />
              Delete File?
            </DialogTitle>
            <DialogClose />
          </DialogHeader>

          <div>
            <p className="text-sm text-neutral-300">
              Are you sure you want to delete{" "}
              <span className="font-mono font-medium">{deleteConfirmation.fileName}</span>?
            </p>
            <p className="text-xs text-neutral-500 mt-2">
              This action cannot be undone.
            </p>
          </div>

          <div className="flex gap-2 justify-end pt-2">
            <button
              type="button"
              onClick={() => setDeleteConfirmation({ isOpen: false })}
              disabled={deleteConfirmation.isDeleting}
              className="text-sm px-3 py-2 border border-neutral-700 text-neutral-200 rounded hover:border-neutral-500 disabled:opacity-60 transition-colors"
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={confirmDelete}
              disabled={deleteConfirmation.isDeleting}
              className="text-sm px-3 py-2 bg-red-500 text-white rounded hover:bg-red-600 disabled:opacity-60 transition-colors font-medium flex items-center gap-2"
            >
              {deleteConfirmation.isDeleting && (
                <Loader2 className="w-4 h-4 animate-spin" />
              )}
              {deleteConfirmation.isDeleting ? "Deleting..." : "Delete"}
            </button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
