import { AudioWaveform, CheckCircle2, CircleAlert, Cloud, Cpu, ExternalLink, Eye, EyeOff, Fingerprint, HardDrive, KeyRound, Loader2, PlugZap, Sparkles, Trash2 } from "lucide-react";
import { type FormEvent, type ReactNode, useEffect, useState } from "react";

import { messageOf, Spinner, useVoice } from "./context";
import {
  clearRecognizerSettings, type CredentialMode, getRecognizerSettings, getVoiceStatus, type RecognizerSettings, type RecognizerTest,
  saveRecognizerSettings, testRecognizer, type VoiceStatus,
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
          <Service icon={<AudioWaveform />} title="语音识别" name="豆包语音 · 录音文件识别极速版" cloud
            ok={status.recognizer.available} okText="已配置" badText="未配置密钥"
            detail="把录音转成文字，带时间、标点和不同的说话人。录音会上传到火山引擎。" />
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
        <p className="vo-note"><KeyRound />豆包语音的密钥由管理员在这里填写。</p>
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
  rejected: "豆包语音拒绝了这组密钥，请检查是否填对、是否开通了“录音文件识别极速版”（volc.bigasr.auc_turbo）",
  unreachable: "连不上豆包语音，请检查 NAS 能否访问互联网",
  failed: "测试没有通过",
};

function RecognizerForm({ onSaved }: { onSaved: () => void }) {
  const voice = useVoice();
  const [settings, setSettings] = useState<RecognizerSettings>();
  const [mode, setMode] = useState<CredentialMode>("apiKey");
  const [apiKey, setAPIKey] = useState("");
  const [appId, setAppId] = useState("");
  const [accessToken, setAccessToken] = useState("");
  const [reveal, setReveal] = useState(false);
  const [working, setWorking] = useState<"save" | "test" | "clear">();
  const [result, setResult] = useState<RecognizerTest>();
  useEffect(() => {
    void getRecognizerSettings().then((value) => {
      setSettings(value);
      if (value.mode) setMode(value.mode);
      if (value.appId) setAppId(value.appId);
    }).catch((caught) => voice.toast(messageOf(caught, "无法读取设置"), "error"));
  }, [voice]);
  const credentials = mode === "apiKey" ? { mode, apiKey } : { mode, appId, accessToken };
  const filled = mode === "apiKey" ? apiKey.trim() !== "" : appId.trim() !== "" && accessToken.trim() !== "";
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
      setSettings({ provider: "doubao", configured: false });
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
  return (
    <form className="vo-credentials" onSubmit={(event) => void save(event)}>
      <header>
        <KeyRound />
        <div>
          <h3>豆包语音密钥</h3>
          <p>
            {settings.configured
              ? <>当前：{settings.mode === "apiKey" ? "API Key" : `APP ID ${settings.appId}`}，密钥 {settings.secretHint}{settings.updatedBy ? ` · ${settings.updatedBy} 于 ${new Date(settings.updatedAt ?? "").toLocaleDateString("zh-CN")} 设置` : ""}</>
              : "在火山引擎控制台开通“豆包语音 · 录音文件识别极速版”后，把密钥填在这里。只有管理员能看到这一栏，保存后不会再显示完整密钥。"}
          </p>
        </div>
        <a className="vo-link" href="https://www.volcengine.com/docs/6561/1631584" target="_blank" rel="noreferrer">接口说明<ExternalLink /></a>
      </header>
      <div className="vo-segmented wide" role="radiogroup" aria-label="控制台版本">
        <button type="button" role="radio" aria-checked={mode === "apiKey"} className={mode === "apiKey" ? "active" : ""} onClick={() => setMode("apiKey")}>新版控制台 · API Key</button>
        <button type="button" role="radio" aria-checked={mode === "appKey"} className={mode === "appKey" ? "active" : ""} onClick={() => setMode("appKey")}>旧版控制台 · APP ID + Access Token</button>
      </div>
      {mode === "apiKey" ? (
        <label className="vo-field">
          <span className="vo-field-label">API Key</span>
          <span className="vo-secret">
            <input type={reveal ? "text" : "password"} value={apiKey} autoComplete="off" spellCheck={false} placeholder={settings.mode === "apiKey" ? `已保存 ${settings.secretHint}，输入新的以替换` : "粘贴 API Key"} onChange={(event) => setAPIKey(event.target.value)} />
            <button type="button" className="vo-icon-button small" aria-label={reveal ? "隐藏" : "显示"} onClick={() => setReveal(!reveal)}>{reveal ? <EyeOff /> : <Eye />}</button>
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
              <button type="button" className="vo-icon-button small" aria-label={reveal ? "隐藏" : "显示"} onClick={() => setReveal(!reveal)}>{reveal ? <EyeOff /> : <Eye />}</button>
            </span>
          </label>
        </div>
      )}
      {result && (
        <p className={`vo-test ${result.ok ? "ok" : "bad"}`} role="status">
          {result.ok ? <CheckCircle2 /> : <CircleAlert />}
          {result.ok ? "连接成功：豆包语音接受了这组密钥" : testTexts[result.code ?? "failed"]}
          {!result.ok && result.message && <small>{result.message}</small>}
        </p>
      )}
      <footer>
        {settings.configured && <button type="button" className="vo-button subtle danger" disabled={!!working} onClick={() => void clear()}><Trash2 />清除</button>}
        <span />
        {settings.configured && <button type="button" className="vo-button" disabled={!!working} onClick={() => void testSaved()}>{working === "test" ? <Loader2 className="vo-spin" /> : <PlugZap />}测试已保存的密钥</button>}
        <button type="submit" className="vo-button primary" disabled={!!working || !filled}>{working === "save" ? <Loader2 className="vo-spin" /> : null}测试并保存</button>
      </footer>
      <p className="vo-fineprint">保存前会发送一秒钟的静音测试密钥，按豆包语音的计费方式计为一次识别。</p>
    </form>
  );
}
