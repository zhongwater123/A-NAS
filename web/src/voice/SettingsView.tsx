import { AudioWaveform, CheckCircle2, CircleAlert, Cloud, Cpu, Database, ExternalLink, Eye, EyeOff, Fingerprint, HardDrive, KeyRound, Loader2, PlugZap, Sparkles, Trash2 } from "lucide-react";
import { type FormEvent, type ReactNode, useEffect, useState } from "react";

import { messageOf, Spinner, useVoice } from "./context";
import {
  clearRecognizerSettings, type CredentialMode, getRecognizerSettings, getVoiceStatus, type RecognizerCredentials, type RecognizerSettings,
  type RecognizerTest, saveRecognizerSettings, testRecognizer, type VoiceStatus,
} from "./voiceApi";

// SettingsView shows which parts of 语音处理 this NAS has, and lets
// administrators enter Doubao's credentials.
export function SettingsView() {
  const voice = useVoice();
  const [status, setStatus] = useState<VoiceStatus | undefined>(voice.status);
  useEffect(() => { void getVoiceStatus().then(setStatus).catch(() => undefined); }, []);
  return (
    <div className="vo-settings">
      <header className="vo-page-head">
        <span className="vo-page-icon"><PlugZap /></span>
        <div>
          <h2>服务与设置</h2>
          <p>语音处理由三部分组成：云端的豆包语音负责转写，VNAS 的文本模型负责翻译和总结，声纹在这台 NAS 上计算。</p>
        </div>
      </header>
      {!status ? <div className="vo-center"><Spinner /></div> : (
        <div className="vo-service-list">
          <Service icon={<AudioWaveform />} title="语音识别" name="豆包语音 · 录音文件识别 2.0" cloud
            ok={status.recognizer.available} okText="已配置" badText="未配置密钥"
            detail="把录音转成文字，带时间、标点和不同的说话人。录音经火山引擎对象存储中转给豆包语音，识别完即删除。" />
          <Service icon={<Sparkles />} title="翻译与总结" name={status.textModel.name ? `VNAS 的文本模型 · ${status.textModel.name}` : "VNAS 的文本模型"} cloud
            ok={status.textModel.available} okText="可用" badText="未接入"
            detail={status.textModel.available ? "沿用 VNAS 的模型网关，密钥由网关保管。转写的文字会发送给模型。" : "这台 NAS 上还没有运行 VNAS 的模型网关，翻译和总结暂不可用，转写不受影响。"} />
          <Service icon={<Fingerprint />} title="声纹" name={status.voiceprints.name ? status.voiceprints.name.split("+")[0] : "3D-Speaker CAM++"}
            ok={status.voiceprints.available} okText="本机运行" badText="不可用"
            detail="区分并认出发言人。声纹不离开这台 NAS。" />
          <Service icon={<Cpu />} title="音频转换" name="FFmpeg（媒体 Worker）"
            ok={status.decoder.available} okText="本机运行" badText="不可用"
            detail="读取各种格式的录音，转成识别服务和声纹需要的格式。" />
        </div>
      )}
      {voice.admin ? <RecognizerForm onSaved={() => void getVoiceStatus().then(setStatus)} /> : (
        <p className="vo-note"><KeyRound />豆包语音的密钥和中转用的存储桶由管理员在这里填写。</p>
      )}
    </div>
  );
}

function Service({ icon, title, name, cloud, ok, okText, badText, detail }: { icon: ReactNode; title: string; name: string; cloud?: boolean; ok: boolean; okText: string; badText: string; detail: string }) {
  return (
    <article className={`vo-service${ok ? "" : " off"}`}>
      <span className="vo-service-icon">{icon}</span>
      <div>
        <header>
          <strong>{title}</strong>
          <span className={`vo-where${cloud ? " cloud" : ""}`}>{cloud ? <Cloud /> : <HardDrive />}{cloud ? "云端" : "本机"}</span>
          <span className={`vo-badge ${ok ? "ok" : "off"}`}>{ok ? <CheckCircle2 /> : <CircleAlert />}{ok ? okText : badText}</span>
        </header>
        <p className="vo-service-name">{name}</p>
        <p>{detail}</p>
      </div>
    </article>
  );
}

const testTexts: Record<string, string> = {
  unconfigured: "还没有填写密钥",
  rejected: "豆包语音拒绝了这组密钥，请检查是否填对、是否开通了“录音文件识别 2.0”（volc.seedasr.auc）",
  unreachable: "连不上豆包语音，请检查 NAS 能否访问互联网",
  storage_rejected: "对象存储拒绝了访问：请检查地域、存储桶名称，以及这组 Access Key 能否在桶里写入和删除",
  storage_unreachable: "连不上火山引擎对象存储，请检查 NAS 能否访问互联网",
  failed: "测试没有通过",
};

// regionNames are the TOS regions' places, for the region picker.
const regionNames: Record<string, string> = {
  "cn-beijing": "北京", "cn-beijing2": "北京 2", "cn-shanghai": "上海", "cn-guangzhou": "广州", "ap-southeast-1": "柔佛",
};

const regionLabel = (region: string) => regionNames[region] ? `${regionNames[region]} · ${region}` : region;

function RecognizerForm({ onSaved }: { onSaved: () => void }) {
  const voice = useVoice();
  const [settings, setSettings] = useState<RecognizerSettings>();
  const [mode, setMode] = useState<CredentialMode>("apiKey");
  const [apiKey, setAPIKey] = useState("");
  const [appId, setAppId] = useState("");
  const [accessToken, setAccessToken] = useState("");
  const [region, setRegion] = useState("cn-beijing");
  const [bucket, setBucket] = useState("");
  const [accessKeyId, setAccessKeyId] = useState("");
  const [secretAccessKey, setSecretAccessKey] = useState("");
  const [reveal, setReveal] = useState(false);
  const [working, setWorking] = useState<"save" | "test" | "clear">();
  const [result, setResult] = useState<RecognizerTest>();
  useEffect(() => {
    void getRecognizerSettings().then((value) => {
      setSettings(value);
      if (value.mode) setMode(value.mode);
      if (value.appId) setAppId(value.appId);
      if (value.storage) {
        setRegion(value.storage.region);
        setBucket(value.storage.bucket);
      }
    }).catch((caught) => voice.toast(messageOf(caught, "无法读取设置"), "error"));
  }, [voice]);
  const storage = { region, bucket: bucket.trim(), accessKeyId: accessKeyId.trim(), secretAccessKey: secretAccessKey.trim() };
  const credentials: RecognizerCredentials = mode === "apiKey" ? { mode, apiKey, storage } : { mode, appId, accessToken, storage };
  const keysFilled = mode === "apiKey" ? apiKey.trim() !== "" : appId.trim() !== "" && accessToken.trim() !== "";
  const filled = keysFilled && storage.bucket !== "" && storage.accessKeyId !== "" && storage.secretAccessKey !== "";
  const save = async (event: FormEvent) => {
    event.preventDefault();
    setWorking("save");
    setResult(undefined);
    try {
      const test = await testRecognizer(credentials);
      setResult(test);
      if (!test.ok) return;
      setSettings(await saveRecognizerSettings(credentials));
      setAPIKey("");
      setAccessToken("");
      setAccessKeyId("");
      setSecretAccessKey("");
      voice.toast("已保存，排队的录音会自动开始");
      onSaved();
    } catch (caught) {
      voice.toast(messageOf(caught, "保存失败"), "error");
    } finally {
      setWorking(undefined);
    }
  };
  const testSaved = async () => {
    setWorking("test");
    try { setResult(await testRecognizer()); } catch (caught) { voice.toast(messageOf(caught, "测试失败"), "error"); } finally { setWorking(undefined); }
  };
  const clear = async () => {
    setWorking("clear");
    try {
      await clearRecognizerSettings();
      setSettings({ provider: "doubao", configured: false, regions: settings?.regions ?? [] });
      setResult(undefined);
      voice.toast("已清除密钥");
      onSaved();
    } catch (caught) {
      voice.toast(messageOf(caught, "清除失败"), "error");
    } finally {
      setWorking(undefined);
    }
  };
  if (!settings) return <div className="vo-center"><Spinner /></div>;
  const saved = settings.storage;
  const secretToggle = <button type="button" className="vo-icon-button small" aria-label={reveal ? "隐藏" : "显示"} onClick={() => setReveal(!reveal)}>{reveal ? <EyeOff /> : <Eye />}</button>;
  return (
    <form className="vo-credentials" onSubmit={(event) => void save(event)}>
      <header>
        <KeyRound />
        <div>
          <h3>豆包语音与录音中转</h3>
          <p>
            {settings.configured
              ? <>当前：{settings.mode === "apiKey" ? "API Key" : `APP ID ${settings.appId}`} {settings.secretHint}，经 TOS {saved ? `${regionLabel(saved.region)} / ${saved.bucket}` : ""} 中转{settings.updatedBy ? ` · ${settings.updatedBy} 于 ${new Date(settings.updatedAt ?? "").toLocaleDateString("zh-CN")} 设置` : ""}</>
              : "在火山引擎开通“豆包语音 · 录音文件识别 2.0”和对象存储 TOS 后填在这里。只有管理员能看到这一栏，保存后不会再显示完整密钥。"}
          </p>
        </div>
        <a className="vo-link" href="https://docs.volcengine.com/docs/DoubaoVoice/task-submission-http-1?lang=zh" target="_blank" rel="noreferrer">接口说明<ExternalLink /></a>
      </header>
      <section className="vo-credential-part" aria-label="豆包语音密钥">
        <h4><AudioWaveform />豆包语音密钥</h4>
        <div className="vo-segmented wide" role="radiogroup" aria-label="控制台版本">
          <button type="button" role="radio" aria-checked={mode === "apiKey"} className={mode === "apiKey" ? "active" : ""} onClick={() => setMode("apiKey")}>新版控制台 · API Key</button>
          <button type="button" role="radio" aria-checked={mode === "appKey"} className={mode === "appKey" ? "active" : ""} onClick={() => setMode("appKey")}>旧版控制台 · APP ID + Access Token</button>
        </div>
        {mode === "apiKey" ? (
          <label className="vo-field">
            <span className="vo-field-label">API Key</span>
            <span className="vo-secret">
              <input type={reveal ? "text" : "password"} value={apiKey} autoComplete="off" spellCheck={false} placeholder={settings.mode === "apiKey" ? `已保存 ${settings.secretHint}，重新输入以保存修改` : "粘贴 API Key"} onChange={(event) => setAPIKey(event.target.value)} />
              {secretToggle}
            </span>
          </label>
        ) : (
          <div className="vo-option-row">
            <label className="vo-field">
              <span className="vo-field-label">APP ID</span>
              <input value={appId} autoComplete="off" spellCheck={false} placeholder="例如 123456789" onChange={(event) => setAppId(event.target.value)} />
            </label>
            <label className="vo-field">
              <span className="vo-field-label">Access Token</span>
              <span className="vo-secret">
                <input type={reveal ? "text" : "password"} value={accessToken} autoComplete="off" spellCheck={false} placeholder={settings.mode === "appKey" ? `已保存 ${settings.secretHint}` : "粘贴 Access Token"} onChange={(event) => setAccessToken(event.target.value)} />
                {secretToggle}
              </span>
            </label>
          </div>
        )}
      </section>
      <section className="vo-credential-part" aria-label="录音中转">
        <h4><Database />录音中转 · 火山引擎对象存储 TOS</h4>
        <p className="vo-credential-why">豆包语音只从链接读取录音，而这台 NAS 不在公网上。识别时，A-NAS 把转好的录音放进这个存储桶，交给豆包语音一个 24 小时有效的签名链接，识别结束立即删除。</p>
        <div className="vo-option-row">
          <label className="vo-field">
            <span className="vo-field-label">地域</span>
            <select value={region} onChange={(event) => setRegion(event.target.value)}>
              {(settings.regions.length ? settings.regions : [region]).map((value) => <option key={value} value={value}>{regionLabel(value)}</option>)}
            </select>
          </label>
          <label className="vo-field">
            <span className="vo-field-label">存储桶</span>
            <input value={bucket} autoComplete="off" spellCheck={false} placeholder="例如 family-voice" onChange={(event) => setBucket(event.target.value)} />
          </label>
        </div>
        <div className="vo-option-row">
          <label className="vo-field">
            <span className="vo-field-label">Access Key ID</span>
            <input value={accessKeyId} autoComplete="off" spellCheck={false} placeholder={saved ? `已保存 ${saved.accessKeyHint}，重新输入` : "AKLT…"} onChange={(event) => setAccessKeyId(event.target.value)} />
          </label>
          <label className="vo-field">
            <span className="vo-field-label">Secret Access Key</span>
            <span className="vo-secret">
              <input type={reveal ? "text" : "password"} value={secretAccessKey} autoComplete="off" spellCheck={false} placeholder={saved ? `已保存 ${saved.secretHint}` : "粘贴 Secret Access Key"} onChange={(event) => setSecretAccessKey(event.target.value)} />
              {secretToggle}
            </span>
          </label>
        </div>
        <p className="vo-fineprint">建议用只能读写这个桶的子账号密钥，并在桶上为 a-nas-voice/ 前缀设置 1 天后删除的生命周期规则，清理意外留下的文件。</p>
      </section>
      {result && (
        <p className={`vo-test ${result.ok ? "ok" : "bad"}`} role="status">
          {result.ok ? <CheckCircle2 /> : <CircleAlert />}
          {result.ok
            ? result.queued ? "连接成功：录音已经中转，豆包语音接受了测试任务（仍在排队）" : "连接成功：录音经存储桶中转，豆包语音完成了测试识别"
            : testTexts[result.code ?? "failed"]}
          {!result.ok && result.message && <small>{result.message}</small>}
        </p>
      )}
      <footer>
        {settings.configured && <button type="button" className="vo-button subtle danger" disabled={!!working} onClick={() => void clear()}><Trash2 />清除</button>}
        <span />
        {settings.configured && <button type="button" className="vo-button" disabled={!!working} onClick={() => void testSaved()}>{working === "test" ? <Loader2 className="vo-spin" /> : <PlugZap />}测试已保存的设置</button>}
        <button type="submit" className="vo-button primary" disabled={!!working || !filled}>{working === "save" ? <Loader2 className="vo-spin" /> : null}测试并保存</button>
      </footer>
      <p className="vo-fineprint">保存前会把一秒钟的静音放进存储桶、提交识别，按豆包语音的计费方式计为一次识别；测试文件随后删除。</p>
    </form>
  );
}
