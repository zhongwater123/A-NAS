import { ArrowRight, Camera, Database, HardDrive, Images, Laptop, Usb, UsersRound } from "lucide-react";

import { useBackup } from "./BackupCenter";

// ExternalPage says plainly what backing up to another disk will be and that
// it is not available yet; nothing on it pretends to work.
export function ExternalPage() {
  const { navigate } = useBackup();
  return (
    <div className="bk-page bk-external-page">
      <header className="bk-page-head">
        <div><h2>外部备份 <span className="bk-chip large">规划中</span></h2><p>把数据复制到另一块硬盘，防范硬盘损坏。</p></div>
      </header>

      <section className="bk-card bk-external-hero" aria-label="外部备份示意">
        <div className="bk-flow" aria-hidden="true">
          <div className="bk-flow-node">
            <span className="bk-flow-disk source"><HardDrive /></span>
            <strong>数据盘</strong>
            <small>文件与快照</small>
          </div>
          <div className="bk-flow-link"><i /><span>复制</span><i /></div>
          <div className="bk-flow-node planned">
            <span className="bk-flow-disk"><Usb /></span>
            <strong>外接硬盘</strong>
            <small>USB</small>
          </div>
          <span className="bk-flow-or">或</span>
          <div className="bk-flow-node planned">
            <span className="bk-flow-disk"><Database /></span>
            <strong>第二块硬盘</strong>
            <small>内置</small>
          </div>
        </div>
        <div className="bk-external-copy">
          <h3>快照不能代替备份</h3>
          <p>
            目前快照和文件保存在同一块数据盘上：能找回误删、误改的文件，但这块硬盘损坏时会一起丢失。
            外部备份会把数据另存一份到别的硬盘上。这项功能还在规划中，目前不能使用。
          </p>
        </div>
      </section>

      <div className="bk-planned-grid">
        <section className="bk-card bk-planned">
          <h4>备份到哪里</h4>
          <ul>
            <li><Usb /><span><strong>USB 外接硬盘</strong><small>需要先支持外接硬盘的识别、挂载与安全移除</small></span></li>
            <li><Database /><span><strong>第二块内置硬盘</strong><small>把另一块 SATA 或 NVMe 硬盘初始化为备份盘</small></span></li>
          </ul>
        </section>
        <section className="bk-card bk-planned">
          <h4>会备份什么</h4>
          <ul>
            <li><HardDrive /><span><strong>个人空间与共享空间</strong><small>全部文件，以快照为一致的来源</small></span></li>
            <li><Images /><span><strong>相册</strong><small>原图与相册信息；缩略图和 AI 结果可重新生成</small></span></li>
            <li><UsersRound /><span><strong>账号与权限</strong><small>恢复到新硬盘后，成员和权限保持一致</small></span></li>
          </ul>
        </section>
      </div>

      <section className="bk-card bk-advice" aria-label="在此之前">
        <span className="bk-advice-icon"><Laptop /></span>
        <div>
          <h4>在此之前</h4>
          <p>请把重要的照片和文档另外复制一份到电脑或外接设备上，并为每个空间开启定时快照。</p>
        </div>
        <button type="button" className="bk-secondary small" onClick={() => navigate("schedules")}><Camera />开启定时快照<ArrowRight /></button>
      </section>
    </div>
  );
}
