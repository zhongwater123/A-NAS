// Pair only an unambiguous same-directory basename in one selection. The
// server additionally checks original Apple identifiers when they exist. A
// video on its own, a MOV among them, is uploaded as it is: the server
// completes the stills in the library still waiting for that Live Photo
// video, by Apple's identifier, and keeps any other video as a video.
export interface UploadGroup {
  file: File;
  motion?: File;
  video?: boolean;
  error?: string;
}

const ambiguousName = "同名文件不唯一，请分开选择每组 Live Photo 原件";
const videoName = /\.(mp4|m4v|mov|qt|3gp|3g2|mkv|webm|avi|mts|m2ts)$/i;

export const isVideoFile = (file: File) => file.type.startsWith("video/") || videoName.test(file.name);

export function groupPhotoUploads(files: File[]): UploadGroup[] {
  const key = (file: File) => (file.webkitRelativePath || file.name).replace(/\.[^.]+$/, "").toLowerCase();
  const isMovie = (file: File) => /\.mov$/i.test(file.name);
  const isStill = (file: File) => /\.(jpe?g|heic|heif)$/i.test(file.name);
  const consumed = new Set<File>();
  const ambiguous = new Set<File>();
  const groups: UploadGroup[] = [];
  for (const file of files) {
    if (isVideoFile(file)) continue;
    const movies = isStill(file) ? files.filter((candidate) => isMovie(candidate) && key(candidate) === key(file)) : [];
    const stills = files.filter((candidate) => isStill(candidate) && key(candidate) === key(file));
    if (movies.length && (movies.length !== 1 || stills.length !== 1)) {
      groups.push({ file, error: ambiguousName });
      for (const movie of movies) ambiguous.add(movie);
    } else {
      const motion = movies[0];
      if (motion) consumed.add(motion);
      groups.push({ file, motion });
    }
  }
  for (const file of files)
    if (isVideoFile(file) && !consumed.has(file))
      groups.push(ambiguous.has(file) ? { file, error: ambiguousName } : { file, video: true });
  return groups;
}
