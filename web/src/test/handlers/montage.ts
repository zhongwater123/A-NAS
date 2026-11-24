// The montage center (剪辑中心): what its song page reads of the device
// besides a project. Each test serves its own projects, songs and jobs.
import type { MontageSongRules } from "../../api.generated";
import { http } from "../server";

// songRules are a device's rules of a song: some of the audio files its music
// library indexes, and the montage center's limits.
export const songRules: MontageSongRules = {
  extensions: [".ape", ".flac", ".m4a", ".mp3", ".wav"],
  maxBytes: 100 << 20,
  minSeconds: 30,
  maxSeconds: 480,
};

// songRulesHandler answers the rules the song page checks an upload by.
export const songRulesHandler = (rules: MontageSongRules = songRules) =>
  http.get("/api/v1/montage/song-rules", ({ response }) => response(200).json(rules));
