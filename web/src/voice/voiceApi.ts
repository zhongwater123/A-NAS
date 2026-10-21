import { request } from "../api";

export type RecordingStatus = "queued" | "processing" | "waiting" | "ready" | "failed";
export type RecordingStage = "preparing" | "recognizing" | "identifying";
export type WaitReason = "sign_in" | "recognizer" | "busy";
export type JobStatus = "queued" | "running" | "ready" | "failed";

export interface Failure { code: string; message?: string }

export interface Recording {
  id: string;
  fileId: string;
  spaceId: string;
  name: string;
  title: string;
  titleEdited: boolean;
  sizeBytes: number;
  durationMs: number;
  language: string;
  template: string;
  translateTo: string;
  hotwords: string[];
  status: RecordingStatus;
  stage?: RecordingStage;
  waiting?: WaitReason;
  progress: number;
  error?: Failure;
  // Browsers cannot play the file: its audio streams decoded from a start.
  transcode: boolean;
  speakers: number;
  people: string[];
  preview?: string;
  textRevision: number;
  namesRevision: number;
  voiceprints: boolean;
  createdAt: string;
  updatedAt: string;
  processedAt?: string;
}

export interface Utterance { seq: number; startMs: number; endMs: number; speaker: string; text: string; edited: boolean }

export interface Suggestion { personId: string; name: string; score: number }

export interface Speaker {
  key: string;
  number: number;
  label: string;
  personId?: string;
  assigned?: "auto" | "user";
  score?: number;
  suggestion?: Suggestion;
  speechMs: number;
  voiceprint: boolean;
}

export interface TranslatedLine { seq: number; text: string }

export interface Translation {
  language: string;
  status: JobStatus;
  error?: Failure;
  revision: number;
  model?: string;
  progress: number;
  lines: TranslatedLine[];
  updatedAt: string;
}

export interface SummarySection { key: string; title: string; items: string[] }
export interface ActionItem { task: string; owner?: string; due?: string }
export interface SummaryContent { title: string; overview: string; sections: SummarySection[]; actions: ActionItem[]; keywords: string[] }

export interface Summary {
  template: string;
  status: JobStatus;
  error?: Failure;
  textRevision: number;
  namesRevision: number;
  model?: string;
  content?: SummaryContent;
  updatedAt: string;
}

export interface RecordingDetail extends Recording {
  utterances: Utterance[];
  speakerList: Speaker[];
  translations: Translation[];
  summaries: Summary[];
}

export interface Person { id: string; name: string; samples: number; recordings: number; speechMs: number; createdAt: string; updatedAt: string }

export interface Component { available: boolean; name?: string }
export interface Template { id: string; name: string; description: string; sections: Array<{ key: string; title: string }>; actions: boolean }
export interface Language { code: string; name: string }

export interface VoiceStatus {
  recognizer: Component;
  textModel: Component;
  voiceprints: Component;
  decoder: Component;
  templates: Template[];
  languages: Language[];
  spokenLanguages: Language[];
}

export type CredentialMode = "apiKey" | "appKey";

export interface RecognizerSettings {
  provider: "doubao";
  configured: boolean;
  mode?: CredentialMode;
  appId?: string;
  secretHint?: string;
  // storage is the TOS bucket that relays recordings to Doubao.
  storage?: { region: string; bucket: string; accessKeyHint: string; secretHint: string };
  regions: string[];
  updatedBy?: string;
  updatedAt?: string;
}

export interface StorageInput { region: string; bucket: string; accessKeyId: string; secretAccessKey: string }
export interface RecognizerCredentials { mode: CredentialMode; apiKey?: string; appId?: string; accessToken?: string; storage: StorageInput }
export interface RecognizerTest {
  ok: boolean;
  code?: "unconfigured" | "rejected" | "unreachable" | "storage_rejected" | "storage_unreachable" | "failed";
  message?: string;
  // queued: Doubao took the test task but had not finished it yet.
  queued?: boolean;
}

export interface ImportOptions { fileId: string; language: string; template: string; translateTo: string; hotwords: string[] }

const base = "/api/v1/voice";
const id = encodeURIComponent;
const json = (method: string, value: unknown): RequestInit => ({ method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(value) });
const recording = (recordingId: string) => `${base}/recordings/${id(recordingId)}`;

export const getVoiceStatus = () => request<VoiceStatus>(`${base}/status`);
export const listRecordings = async () => (await request<{ recordings: Recording[] }>(`${base}/recordings`)).recordings;
export const importRecording = (options: ImportOptions) => request<Recording>(`${base}/recordings`, json("POST", options), true);
export const getRecording = (recordingId: string) => request<RecordingDetail>(recording(recordingId));
export const renameRecording = (recordingId: string, title: string) => request<Recording>(recording(recordingId), json("PATCH", { title }), true);
export const deleteRecording = (recordingId: string) => request<void>(recording(recordingId), { method: "DELETE" }, true);
export const retryRecording = (recordingId: string) => request<Recording>(`${recording(recordingId)}/retry`, { method: "POST" }, true);
export const audioURL = (recordingId: string, start?: number) => `${recording(recordingId)}/audio${start === undefined ? "" : `?start=${start.toFixed(2)}`}`;

export const editUtterance = (recordingId: string, seq: number, change: { text?: string; speaker?: string }) =>
  request<RecordingDetail>(`${recording(recordingId)}/utterances/${seq}`, json("PATCH", change), true);
export const assignSpeaker = (recordingId: string, key: string, person: { name?: string; personId?: string }) =>
  request<RecordingDetail>(`${recording(recordingId)}/speakers/${id(key)}`, json("PUT", person), true);
export const rejectPerson = (recordingId: string, key: string, personId: string) =>
  request<RecordingDetail>(`${recording(recordingId)}/speakers/${id(key)}/reject`, json("POST", { personId }), true);
export const mergeSpeakers = (recordingId: string, key: string, into: string) =>
  request<RecordingDetail>(`${recording(recordingId)}/speakers/${id(key)}/merge`, json("POST", { into }), true);

export const requestTranslation = (recordingId: string, language: string) =>
  request<RecordingDetail>(`${recording(recordingId)}/translations`, json("POST", { language }), true);
export const deleteTranslation = (recordingId: string, language: string) =>
  request<void>(`${recording(recordingId)}/translations/${id(language)}`, { method: "DELETE" }, true);
export const requestSummary = (recordingId: string, template: string) =>
  request<RecordingDetail>(`${recording(recordingId)}/summaries`, json("POST", { template }), true);
export const deleteSummary = (recordingId: string, template: string) =>
  request<void>(`${recording(recordingId)}/summaries/${id(template)}`, { method: "DELETE" }, true);

export const listPeople = async () => (await request<{ people: Person[] }>(`${base}/people`)).people;
export const renamePerson = (personId: string, name: string) => request<Person>(`${base}/people/${id(personId)}`, json("PATCH", { name }), true);
export const deletePerson = (personId: string) => request<void>(`${base}/people/${id(personId)}`, { method: "DELETE" }, true);
export const mergePeople = (personId: string, into: string) => request<Person>(`${base}/people/${id(personId)}/merge`, json("POST", { into }), true);

export const getRecognizerSettings = () => request<RecognizerSettings>(`${base}/settings/recognizer`);
export const saveRecognizerSettings = (credentials: RecognizerCredentials) =>
  request<RecognizerSettings>(`${base}/settings/recognizer`, json("PUT", credentials), true);
export const clearRecognizerSettings = () => request<void>(`${base}/settings/recognizer`, { method: "DELETE" }, true);
export const testRecognizer = (credentials?: RecognizerCredentials) =>
  request<RecognizerTest>(`${base}/settings/recognizer/test`, credentials ? json("POST", credentials) : { method: "POST" }, true);

// busy says whether a recording still changes on its own: processing, or a
// translation or summary on the way.
export function busy(recording: Recording | RecordingDetail): boolean {
  if (recording.status === "queued" || recording.status === "processing" || recording.status === "waiting") return true;
  if ("translations" in recording) {
    return recording.translations.some((item) => item.status === "queued" || item.status === "running")
      || recording.summaries.some((item) => item.status === "queued" || item.status === "running");
  }
  return false;
}
