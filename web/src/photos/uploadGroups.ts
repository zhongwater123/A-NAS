// Pair only an unambiguous same-directory basename in one selection. The
// server additionally checks original Apple identifiers when they exist.
export function groupPhotoUploads(files: File[]): { file: File; motion?: File; error?: string }[] {
  const key = (file: File) => (file.webkitRelativePath || file.name).replace(/\.[^.]+$/, "").toLowerCase();
  const isMovie = (file: File) => /\.mov$/i.test(file.name);
  const isStill = (file: File) => /\.(jpe?g|heic|heif)$/i.test(file.name);
  const consumed = new Set<File>();
  const groups: { file: File; motion?: File; error?: string }[] = [];
  for (const file of files) {
    if (isMovie(file)) continue;
    const movies = isStill(file) ? files.filter((candidate) => isMovie(candidate) && key(candidate) === key(file)) : [];
    const stills = files.filter((candidate) => isStill(candidate) && key(candidate) === key(file));
    if (movies.length && (movies.length !== 1 || stills.length !== 1)) {
      groups.push({ file, error: "同名文件不唯一，请分开选择每组 Live Photo 原件" });
    } else {
      const motion = movies[0];
      if (motion) consumed.add(motion);
      groups.push({ file, motion });
    }
  }
  for (const file of files) if (isMovie(file) && !consumed.has(file)) groups.push({ file, error: "请同时选择对应的同名 JPEG 或 HEIC 照片" });
  return groups;
}
