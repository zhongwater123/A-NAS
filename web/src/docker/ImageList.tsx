import { Layers, RefreshCw } from "lucide-react";

import { formatBytes } from "../lib/bytes";
import { ContainerImage, ContainerSnapshot, ContainerSummary } from "./api";
import { DetailTab } from "./ContainerDetail";
import { formatAgo, formatDateTime, imageUsers, matchesImageQuery, shortID, splitReference } from "./model";
import { Empty } from "./parts";

interface ImagePageProps {
  snapshot: ContainerSnapshot;
  query: string;
  refreshing: boolean;
  // Search results show matching images under the containers, without a page heading.
  embedded?: boolean;
  onRefresh: () => Promise<void>;
  onOpen: (item: ContainerSummary, tab?: DetailTab) => void;
}

interface Row {
  image: ContainerImage;
  users: ContainerSummary[];
}

export function ImagePage({ snapshot, query, refreshing, embedded, onRefresh, onOpen }: ImagePageProps) {
  const rows: Row[] = snapshot.images
    .filter((image) => matchesImageQuery(image, query))
    .sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt))
    .map((image) => ({ image, users: imageUsers(image, snapshot.containers) }));

  if (embedded) {
    if (rows.length === 0) return null;
    return (
      <div className="dk-page dk-page-more">
        <section className="dk-group" aria-label="镜像">
          <header className="dk-group-head">
            <span className="dk-tile images">
              <Layers />
            </span>
            <h3>镜像</h3>
            <span className="dk-group-count">{rows.length} 个</span>
          </header>
          <ImageTable rows={rows} onOpen={onOpen} />
        </section>
      </div>
    );
  }

  const unused = rows.filter((row) => row.users.length === 0).length;
  return (
    <div className="dk-page">
      <header className="dk-page-head">
        <div className="dk-page-title">
          <h2>镜像</h2>
          {rows.length > 0 && (
            <span className="dk-page-count">
              {rows.length} 个{unused > 0 && ` · ${unused} 个未被容器使用`}
            </span>
          )}
        </div>
        <button
          className="dk-icon-btn ghost"
          aria-label="刷新"
          title="刷新"
          disabled={refreshing}
          onClick={() => void onRefresh()}
        >
          <RefreshCw className={refreshing ? "dk-spin" : ""} />
        </button>
      </header>
      {rows.length === 0 ? <Empty icon={<Layers />} title="还没有镜像" /> : <ImageTable rows={rows} onOpen={onOpen} />}
    </div>
  );
}

function ImageTable({ rows, onOpen }: { rows: Row[]; onOpen: ImagePageProps["onOpen"] }) {
  return (
    <div className="dk-table" role="table" aria-label="镜像列表">
      <div className="dk-table-row head" role="row">
        <span role="columnheader">镜像</span>
        <span role="columnheader">使用它的容器</span>
        <span role="columnheader" className="num">
          大小
        </span>
        <span role="columnheader">创建于</span>
      </div>
      {rows.map(({ image, users }) => {
        const reference = image.tags[0] ? splitReference(image.tags[0]) : undefined;
        return (
          <div className="dk-table-row" role="row" key={image.id} aria-label={image.tags[0] ?? shortID(image.id)}>
            <span role="cell" className="dk-image-name">
              <span className={`dk-image-glyph ${users.length ? "" : "unused"}`}>
                <Layers />
              </span>
              <span className="dk-image-text">
                <strong className={reference ? "" : "untagged"} title={image.tags.join("\n") || undefined}>
                  {reference?.repository ?? "未标记的镜像"}
                </strong>
                <span className="dk-image-sub">
                  {reference && <span className="dk-tag">{reference.tag}</span>}
                  {reference?.registry && <span className="dk-registry">{reference.registry}</span>}
                  <code title={image.id}>{shortID(image.id)}</code>
                  {image.tags.length > 1 && <small>另有 {image.tags.length - 1} 个标签</small>}
                </span>
              </span>
            </span>
            <span role="cell" className="dk-image-users">
              {users.length > 0 ? (
                users.map((user) => (
                  <button
                    key={user.id}
                    className={`dk-chip ${user.state === "running" ? "running" : ""}`}
                    title={`查看容器 ${user.name}`}
                    onClick={() => onOpen(user)}
                  >
                    <i />
                    {user.name}
                  </button>
                ))
              ) : (
                <span className="dk-faint">未使用</span>
              )}
            </span>
            <span role="cell" className="num">
              {formatBytes(image.sizeBytes)}
            </span>
            <span role="cell" title={formatDateTime(image.createdAt)}>
              {formatAgo(image.createdAt)}
            </span>
          </div>
        );
      })}
    </div>
  );
}
