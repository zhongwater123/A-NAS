import { ArrowRight, Camera, Database, HardDrive, Images, Laptop, Usb, UsersRound } from "lucide-react";

import workspaceStyles from "../ui/workspace.module.css";
import { useBackup } from "./BackupCenter";
import styles from "./backup.module.css";

// ExternalPage says plainly what backing up to another disk will be and that
// it is not available yet; nothing on it pretends to work.
export function ExternalPage() {
  const { navigate, spaces, data } = useBackup();
  const allScheduled = spaces.length > 0 && spaces.every((space) => data[space.id]?.schedule?.enabled);
  return (
    <div className={`${styles.bkPage} bk-external-page`}>
      <header className={styles.bkPageHead}>
        <div>
          <h2>
            外部备份 <span className={`${styles.bkChip} ${styles.large}`}>规划中</span>
          </h2>
          <p>把数据复制到另一块硬盘，防范硬盘损坏。</p>
        </div>
      </header>

      <section className={`${styles.bkCard} ${styles.bkExternalHero}`} aria-label="外部备份示意">
        <div className={styles.bkFlow} aria-hidden="true">
          <div className={styles.bkFlowNode}>
            <span className={`${styles.bkFlowDisk} source`}>
              <HardDrive />
            </span>
            <strong>数据盘</strong>
            <small>文件与快照</small>
          </div>
          <div className={styles.bkFlowLink}>
            <i />
            <span>复制</span>
            <i />
          </div>
          <div className={`${styles.bkFlowNode} ${styles.planned}`}>
            <span className={styles.bkFlowDisk}>
              <Usb />
            </span>
            <strong>外接硬盘</strong>
            <small>USB</small>
          </div>
          <span className={styles.bkFlowOr}>或</span>
          <div className={`${styles.bkFlowNode} ${styles.planned}`}>
            <span className={styles.bkFlowDisk}>
              <Database />
            </span>
            <strong>第二块硬盘</strong>
            <small>内置</small>
          </div>
        </div>
        <div className={styles.bkExternalCopy}>
          <h3>快照不能代替备份</h3>
          <p>
            目前快照和文件保存在同一块数据盘上：能找回误删、误改的文件，但这块硬盘损坏时会一起丢失。
            外部备份会把数据另存一份到别的硬盘上。这项功能还在规划中，目前不能使用。
          </p>
        </div>
      </section>

      <div className={styles.bkPlannedGrid}>
        <section className={`${styles.bkCard} ${styles.bkPlanned}`}>
          <h4>备份到哪里</h4>
          <ul>
            <li>
              <Usb />
              <span>
                <strong>USB 外接硬盘</strong>
                <small>需要先支持外接硬盘的识别、挂载与安全移除</small>
              </span>
            </li>
            <li>
              <Database />
              <span>
                <strong>第二块内置硬盘</strong>
                <small>把另一块 SATA 或 NVMe 硬盘初始化为备份盘</small>
              </span>
            </li>
          </ul>
        </section>
        <section className={`${styles.bkCard} ${styles.bkPlanned}`}>
          <h4>会备份什么</h4>
          <ul>
            <li>
              <HardDrive />
              <span>
                <strong>个人空间与共享空间</strong>
                <small>全部文件，以快照为一致的来源</small>
              </span>
            </li>
            <li>
              <Images />
              <span>
                <strong>相册</strong>
                <small>原图与相册信息；缩略图和 AI 结果可重新生成</small>
              </span>
            </li>
            <li>
              <UsersRound />
              <span>
                <strong>账号与权限</strong>
                <small>恢复到新硬盘后，成员和权限保持一致</small>
              </span>
            </li>
          </ul>
        </section>
      </div>

      <section className={`${styles.bkCard} ${styles.bkAdvice}`} aria-label="在此之前">
        <span className={styles.bkAdviceIcon}>
          <Laptop />
        </span>
        <div>
          <h4>在此之前</h4>
          <p>
            {allScheduled
              ? "请把重要的照片和文档另外复制一份到电脑或外接设备上。每个空间都已开启定时快照，误删的文件可以找回。"
              : "请把重要的照片和文档另外复制一份到电脑或外接设备上，并为每个空间开启定时快照。"}
          </p>
        </div>
        {!allScheduled && (
          <button
            type="button"
            className={`${workspaceStyles.bkSecondary} ${styles.small} ${workspaceStyles.small}`}
            onClick={() => navigate("schedules")}
          >
            <Camera />
            开启定时快照
            <ArrowRight />
          </button>
        )}
      </section>
    </div>
  );
}
