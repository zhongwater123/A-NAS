// A-NAS: not in PC2005-cloud/dsh-pet; added to its copy at 10f6c868b811 by dsh-pet-patches/0021 (MIT, see LICENSE).
// A-NAS: what host/config.ts gives the decision layers (./anim, ./motion-cue, ./broadcast), for the page.
/**
 * dsh-pet's host reads its pets and their animations from configuration files on disk (host/config/read.ts,
 * which the page cannot run). A-NAS has one pet, `main`, whose animations are dsh-pet's own configuration at
 * the pinned commit (../animations.json, which scripts/vendor-dsh-pet.py writes). `readAllConfig` returns
 * that pet in the shape the decision layers take; `flattenPetList` and `findPetInstance` are copied from
 * host/config/read.ts unchanged.
 */
import config from '../animations.json';

/** The finished configuration: entry (the asset root) -> its pets and its animations. */
const page: Record<string, Record<string, unknown>> = {
  main: { pets: [{ id: 'main' }], animations: config.animations },
};

/** The page's finished configuration (dsh-pet reads its user and default files into this shape). */
export function readAllConfig(): Record<string, Record<string, unknown>> {
  return page;
}

/** 拍平全部条目的 pets 为单列表（host 消费端用：桌面宠物列表 / 命令 / 当前桌宠解析） */
export function flattenPetList(merged: Record<string, Record<string, unknown>>): Record<string, unknown>[] {
  const out: Record<string, unknown>[] = [];
  for (const conf of Object.values(merged)) {
    if (Array.isArray(conf?.pets)) out.push(...(conf.pets as Record<string, unknown>[]));
  }
  return out;
}

/** 在完成品聚合里按实例 id 定位宠物及其所属条目（host 内部消费索引）：
 *  条目 key 即素材根（assetRoot）；条目级字段（whisperPrompt/chatMemoryRounds/animations）随条目取
 *  （唯一例外：whisperPrompt 可被实例覆盖——调用方要生效值请走 ./persona 的 petPersona，别直接读 conf）。 */
export function findPetInstance(
  merged: Record<string, Record<string, unknown>>,
  petId: string,
): { entry: string; conf: Record<string, unknown>; pet: Record<string, unknown> } | undefined {
  for (const [entry, conf] of Object.entries(merged)) {
    const pets = Array.isArray(conf?.pets) ? (conf.pets as Record<string, unknown>[]) : [];
    const found = pets.find((p) => String(p.id) === petId);
    if (found) return { entry, conf, pet: found };
  }
  return undefined;
}
