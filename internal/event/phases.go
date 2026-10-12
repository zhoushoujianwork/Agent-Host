package event

// PhaseDefinition is shared by live text, replay and channel feedback. Features
// opt into progress rendering here; source text cannot invent phase labels.
type PhaseDefinition struct {
	Label       string
	Failure     string
	Interrupted string
	Channel     bool
	Repeatable  bool
}

var phases = map[string]PhaseDefinition{
	"recall":           {Label: "检索记忆", Channel: true},
	"execute":          {Label: "AI执行", Channel: true, Repeatable: true},
	"remember":         {Label: "Jev判断", Channel: true},
	"persist":          {Label: "保存记忆", Channel: true},
	"evidence":         {Label: "整理证据", Channel: true},
	"extract":          {Label: "提取经验", Channel: true},
	"assess":           {Label: "Jev评估", Channel: true},
	"candidates":       {Label: "保存候选", Channel: true},
	"knowledge_search": {Label: "检索知识", Failure: "检索失败", Interrupted: "检索中断", Channel: true, Repeatable: true},
	"knowledge_read":   {Label: "阅读资料", Failure: "读取失败", Interrupted: "读取中断", Channel: true, Repeatable: true},
}

func Phase(name string) (PhaseDefinition, bool) {
	p, ok := phases[name]
	return p, ok
}

// PhaseReaction projects only meaningful transitions. Successful child finishes
// remain in the journal; the next actual activity supplies the next marker.
func PhaseReaction(e Event) (string, bool) {
	p, ok := Phase(e.Phase)
	if !ok || !p.Channel {
		return "", false
	}
	switch e.Type {
	case "phase.started", "phase.resumed":
		return p.Label, true
	case "phase.finished":
		if e.Status == "failed" && p.Failure != "" {
			return p.Failure, true
		}
		if (e.Status == "unknown" || e.Status == "canceled") && p.Interrupted != "" {
			return p.Interrupted, true
		}
	}
	return "", false
}
