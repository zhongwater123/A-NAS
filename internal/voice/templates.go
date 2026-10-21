package voice

// Template is a scene a summary is written for: the sections the text model
// fills and whether it lists the tasks agreed on.
type Template struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Sections    []TemplateSection `json:"sections"`
	Actions     bool              `json:"actions"`
	// Focus tells the text model what matters in this scene.
	Focus string `json:"-"`
}

type TemplateSection struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Hint  string `json:"-"`
}

// Templates are the scenes, in the order they are offered.
var Templates = []Template{
	{
		ID: "general", Name: "通用摘要", Description: "提炼要点与待办，适合任何录音",
		Focus:    "提炼录音中真正重要的信息，去掉寒暄与重复。",
		Sections: []TemplateSection{{Key: "points", Title: "要点", Hint: "按重要性排列的关键信息，每条一句话"}},
		Actions:  true,
	},
	{
		ID: "meeting", Name: "会议纪要", Description: "议题、结论与待办事项，写清谁负责",
		Focus: "这是一次会议。按议题整理讨论，写清最后的结论与分歧，待办要有负责人与期限（录音中提到时）。",
		Sections: []TemplateSection{
			{Key: "topics", Title: "议题与讨论", Hint: "每条以议题开头，概括各方观点"},
			{Key: "decisions", Title: "决定事项", Hint: "会上明确达成的结论"},
			{Key: "risks", Title: "问题与风险", Hint: "尚未解决的问题、分歧与风险"},
		},
		Actions: true,
	},
	{
		ID: "lecture", Name: "课堂笔记", Description: "知识点结构、重点难点和课后复习",
		Focus: "这是一节课或培训。像认真听讲的学生那样整理笔记：知识点要有层次，保留老师给出的例子与定义。",
		Sections: []TemplateSection{
			{Key: "concepts", Title: "知识要点", Hint: "按讲授顺序列出概念、定义与结论"},
			{Key: "keypoints", Title: "重点与难点", Hint: "老师强调的内容和容易出错的地方"},
			{Key: "examples", Title: "例子与案例", Hint: "课上举的例子及其说明的道理"},
			{Key: "review", Title: "复习问题", Hint: "3 到 5 个可以检验是否掌握的问题"},
		},
	},
	{
		ID: "interview", Name: "访谈记录", Description: "受访者观点、问答要点与原话引用",
		Focus: "这是一次访谈或采访。分清提问者与受访者，保留受访者的核心观点，引用时用原话。",
		Sections: []TemplateSection{
			{Key: "background", Title: "受访者与背景", Hint: "受访者是谁、访谈缘由（录音中提到时）"},
			{Key: "views", Title: "核心观点", Hint: "受访者表达的主要观点"},
			{Key: "qa", Title: "问答要点", Hint: "“问：… 答：…”的形式概括关键问答"},
			{Key: "quotes", Title: "原话摘录", Hint: "值得引用的原话，加引号"},
		},
	},
	{
		ID: "call", Name: "通话摘要", Description: "通话目的、关键信息和双方约定",
		Focus: "这是一次电话或语音通话。记下通话目的，以及时间、地点、金额、电话号码等需要记住的具体信息。",
		Sections: []TemplateSection{
			{Key: "purpose", Title: "通话目的", Hint: "谁为什么打这通电话"},
			{Key: "facts", Title: "关键信息", Hint: "时间、地点、金额、数量、联系方式等具体信息"},
			{Key: "agreements", Title: "双方约定", Hint: "达成的一致与承诺"},
		},
		Actions: true,
	},
	{
		ID: "talk", Name: "讲座 · 播客", Description: "内容提要、核心观点和提到的资源",
		Focus: "这是一场讲座、演讲或一期播客。按内容的先后梳理脉络，提要每条前写开始的时间（mm:ss）。",
		Sections: []TemplateSection{
			{Key: "outline", Title: "内容提要", Hint: "每条以“mm:ss ”开头，按时间顺序"},
			{Key: "insights", Title: "核心观点", Hint: "讲者最想传达的观点"},
			{Key: "resources", Title: "提到的资源", Hint: "书、文章、产品、人名等，没有就留空"},
		},
	},
	{
		ID: "memo", Name: "语音备忘", Description: "把随口说的想法整理成清楚的笔记",
		Focus: "这是一段自言自语的语音备忘。把口语整理成条理清楚的书面笔记，不要遗漏任何想法。",
		Sections: []TemplateSection{
			{Key: "notes", Title: "整理后的笔记", Hint: "按主题归并的想法，每条完整成句"},
			{Key: "ideas", Title: "灵感与待考虑", Hint: "尚未决定、需要再想的事"},
		},
		Actions: true,
	},
}

func templateByID(id string) (Template, bool) {
	for _, template := range Templates {
		if template.ID == id {
			return template, true
		}
	}
	return Template{}, false
}

// Language is a language a transcript can be translated into.
type Language struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Languages are the translation targets, in the order they are offered.
var Languages = []Language{
	{"zh-CN", "简体中文"}, {"zh-TW", "繁體中文"}, {"en", "英语"}, {"ja", "日语"}, {"ko", "韩语"},
	{"fr", "法语"}, {"de", "德语"}, {"es", "西班牙语"}, {"ru", "俄语"}, {"pt", "葡萄牙语"},
	{"it", "意大利语"}, {"ar", "阿拉伯语"}, {"th", "泰语"}, {"vi", "越南语"}, {"id", "印尼语"},
}

func languageByCode(code string) (Language, bool) {
	for _, language := range Languages {
		if language.Code == code {
			return language, true
		}
	}
	return Language{}, false
}

// SpokenLanguages are what the recording may be in. "" lets the recognition
// service tell Mandarin, English and Chinese dialects apart by itself, and is
// the only choice that tells speakers apart (the service separates speakers
// only for Mandarin).
var SpokenLanguages = []Language{
	{"", "自动（普通话、英语与方言）"}, {"yue-CN", "粤语"}, {"en-US", "英语"}, {"ja-JP", "日语"}, {"ko-KR", "韩语"},
	{"fr-FR", "法语"}, {"de-DE", "德语"}, {"es-MX", "西班牙语"}, {"ru-RU", "俄语"}, {"pt-BR", "葡萄牙语"},
	{"it-IT", "意大利语"}, {"th-TH", "泰语"}, {"vi-VN", "越南语"}, {"id-ID", "印尼语"},
}

func spokenLanguage(code string) bool {
	for _, language := range SpokenLanguages {
		if language.Code == code {
			return true
		}
	}
	return false
}
