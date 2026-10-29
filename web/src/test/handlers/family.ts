// 家庭中心: the family as one member sees it, a baby's channel and the
// catalog the window offers.
import type {
  FamilyBaby,
  FamilyCatalog,
  FamilyGrowth,
  FamilyOverview,
  FamilyRecord,
  FamilyVaccineSchedule,
} from "../../api.generated";
import { http } from "../server";

export const familyCatalog: FamilyCatalog = {
  roles: ["爸爸", "妈妈", "爷爷", "奶奶", "外公", "外婆", "哥哥", "姐姐"],
  milestones: ["第一次笑", "第一次翻身"],
  growthStandard: "WS/T 423—2022《7 岁以下儿童生长标准》",
  vaccines: {
    version: "2026",
    title: "国家免疫规划疫苗儿童免疫程序及说明（2026年版）",
    source: "国家疾控局、国家卫生健康委",
    vaccines: [
      { code: "dtap", name: "百白破疫苗", programme: true, doses: [{ dose: 1, ageMonths: 2, ageLabel: "2月龄" }] },
      {
        code: "dtap-ipv-hib",
        name: "五联疫苗（百白破-脊灰-Hib）",
        programme: false,
        covers: ["dtap", "polio"],
        doses: [{ dose: 1, ageMonths: 2, ageLabel: "2月龄" }],
      },
    ],
  },
};

export function familyBaby(overrides: Partial<FamilyBaby> = {}): FamilyBaby {
  return {
    id: "baby_1",
    name: "小米粒",
    nickname: "米米",
    sex: "female",
    birthDate: "2026-03-15",
    age: { years: 0, months: 7, days: 14, totalMonths: 7 },
    avatarVersion: 0,
    version: 1,
    createdAt: "2026-10-01T00:00:00Z",
    createdBy: "user:mom",
    ...overrides,
  };
}

export function familyOverview(overrides: Partial<FamilyOverview> = {}): FamilyOverview {
  return {
    exists: true,
    me: { status: "member", role: "妈妈", guardian: true },
    members: [
      {
        accountId: "user:mom",
        username: "mom",
        role: "妈妈",
        guardian: true,
        joinedAt: "2026-10-01T00:00:00Z",
        active: true,
      },
      {
        accountId: "user:grandma",
        username: "grandma",
        role: "外婆",
        guardian: false,
        joinedAt: "2026-10-02T00:00:00Z",
        active: true,
      },
    ],
    requests: [],
    babies: [familyBaby()],
    deletedBabies: [],
    ...overrides,
  };
}

export function familyRecord(overrides: Partial<FamilyRecord> = {}): FamilyRecord {
  return {
    id: "rec_1",
    babyId: "baby_1",
    kind: "journal",
    occurredOn: "2026-10-28",
    age: { years: 0, months: 7, days: 13, totalMonths: 7 },
    text: "今天第一次笑出了声",
    photos: [],
    authorId: "user:grandma",
    authorName: "grandma",
    createdAt: "2026-10-28T10:00:00Z",
    version: 1,
    comments: [],
    ...overrides,
  };
}

export const emptySchedule: FamilyVaccineSchedule = {
  version: "2026",
  title: "国家免疫规划疫苗儿童免疫程序及说明（2026年版）",
  source: "国家疾控局",
  doses: [],
  plan: [],
  records: [],
};

export interface FamilyOptions {
  overview?: FamilyOverview;
  records?: FamilyRecord[];
  schedule?: FamilyVaccineSchedule;
  growth?: FamilyGrowth;
}

// familyHandlers answers what the window reads when it opens on a channel.
export function familyHandlers(options: FamilyOptions = {}) {
  return [
    http.get("/api/v1/family", ({ response }) => response(200).json(options.overview ?? familyOverview())),
    http.get("/api/v1/family/catalog", ({ response }) => response(200).json(familyCatalog)),
    http.get("/api/v1/family/babies/{babyId}/records", ({ response }) =>
      response(200).json({ records: options.records ?? [] }),
    ),
    http.get("/api/v1/family/babies/{babyId}/vaccines", ({ response }) =>
      response(200).json(options.schedule ?? emptySchedule),
    ),
  ];
}
