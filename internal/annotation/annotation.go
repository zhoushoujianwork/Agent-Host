// Package annotation provides bounded, source-grounded retrieval metadata.
// These rules identify explicit names and a small vocabulary; they do not infer
// arbitrary entities, intentions, truth, or permission from source text.
package annotation

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const Version = "entity-topic-v1"

type Span struct {
	Text   string `json:"text"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Method string `json:"method"`
}

type Object struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Span Span   `json:"span"`
}

type Alias struct {
	Name     string `json:"name"`
	TargetID string `json:"target_id"`
	Span     Span   `json:"span"`
}

type Topic struct {
	Key       string   `json:"key"`
	Span      Span     `json:"span"`
	ObjectIDs []string `json:"object_ids,omitempty"`
}

type Annotation struct {
	Version string   `json:"version"`
	Objects []Object `json:"objects"`
	Aliases []Alias  `json:"aliases"`
	Topics  []Topic  `json:"topics"`
}

type Query struct {
	Annotation      Annotation `json:"annotation"`
	ResolvedIDs     []string   `json:"resolved_ids"`
	Ambiguous       []string   `json:"ambiguous"`
	Unknown         []string   `json:"unknown"`
	resolvedObjects []Object
	text            string
}

var (
	chineseService  = regexp.MustCompile(`[\p{Han}A-Za-z0-9_-]+?服务`)
	latinService    = regexp.MustCompile(`(?i)[a-z][a-z0-9_-]*(?:-service|[ ]+service)\b`)
	aliasSuffix     = regexp.MustCompile(`^[ ]*[（(][ ]*(?:简称|别名|alias)[：: ]*([\p{Han}A-Za-z0-9_-]+)[ ]*[）)]`)
	clausePattern   = regexp.MustCompile(`[^。！？!?；;，,\n]+`)
	periodPattern   = regexp.MustCompile(`(?i)(周期|间隔|每[^。！？!?；;\n]{0,24}(天|日|周|月|年|小时)|every[ ]+[a-z0-9 ]{0,24}(day|week|month|year|hour)|interval|period)`)
	rotationPattern = regexp.MustCompile(`(?i)(轮换|轮替|更换|\brotat(?:e|es|ed|ing|ion)\b)`)
)

// Extract retains source byte offsets. IDs depend on scope, kind, and exact
// canonical spelling, never similarity. Aliases remain separate evidence.
func Extract(scope, text string) Annotation {
	a := Annotation{Version: Version, Objects: []Object{}, Aliases: []Alias{}, Topics: []Topic{}}
	var locations [][2]int
	for _, loc := range chineseService.FindAllStringIndex(text, -1) {
		start := trimServicePrefix(text, loc[0], loc[1])
		name := text[start:loc[1]]
		if validService(name) {
			locations = append(locations, [2]int{start, loc[1]})
		}
	}
	for _, loc := range latinService.FindAllStringIndex(text, -1) {
		if loc[0] > 0 && isNameRune(previousRune(text, loc[0])) {
			continue
		}
		locations = append(locations, [2]int{loc[0], loc[1]})
	}
	sort.Slice(locations, func(i, j int) bool { return locations[i][0] < locations[j][0] })
	for _, loc := range locations {
		if insideAlias(a.Aliases, loc[0], loc[1]) {
			continue
		}
		name := text[loc[0]:loc[1]]
		o := Object{ID: objectID(scope, "service", name), Name: name, Kind: "service", Span: sourceSpan(text, loc[0], loc[1], "explicit-service-name")}
		if !hasObject(a.Objects, o.ID, loc[0]) {
			a.Objects = append(a.Objects, o)
		}
		if match := aliasSuffix.FindStringSubmatchIndex(text[loc[1]:]); match != nil && !negativeAliasClause(text, loc[0], loc[1]+match[1]) {
			start, end := loc[1]+match[2], loc[1]+match[3]
			a.Aliases = append(a.Aliases, Alias{Name: text[start:end], TargetID: o.ID, Span: sourceSpan(text, start, end, "explicit-parenthetical-alias")})
		}
	}
	for _, loc := range clausePattern.FindAllStringIndex(text, -1) {
		extractTopics(&a, text, loc[0], loc[1])
	}
	bindTopics(&a, text)
	return a
}

// Resolve consults only the supplied registry. Callers must supply active,
// same-scope annotations. Alias collisions abstain instead of choosing a name.
func Resolve(scope, text string, records []Annotation) Query {
	q := Query{Annotation: Extract(scope, text), ResolvedIDs: []string{}, Ambiguous: []string{}, Unknown: []string{}, text: text}
	known := map[string]bool{}
	aliases := map[string]map[string]bool{}
	for _, a := range records {
		for _, o := range a.Objects {
			// A foreign-scope object cannot enter resolution even if a caller
			// accidentally supplied annotations from another workspace.
			if o.ID == objectID(scope, o.Kind, o.Name) {
				known[o.ID] = true
			}
		}
	}
	for _, a := range records {
		for _, alias := range a.Aliases {
			if known[alias.TargetID] {
				key := canonical(alias.Name)
				if aliases[key] == nil {
					aliases[key] = map[string]bool{}
				}
				aliases[key][alias.TargetID] = true
			}
		}
	}
	for _, o := range q.Annotation.Objects {
		ids := copySet(aliases[canonical(o.Name)])
		if known[o.ID] {
			ids[o.ID] = true
		}
		resolveName(&q, o.Name, ids, true)
		if len(ids) == 1 {
			for id := range ids {
				o.ID = id
				q.resolvedObjects = append(q.resolvedObjects, o)
			}
		}
	}
	// Only full, bounded alias mentions count. Longest matches suppress a
	// shorter alias within the same span (for example payment vs payment-api).
	type mention struct {
		name       string
		start, end int
	}
	var mentions []mention
	for name := range aliases {
		for _, loc := range literalLocations(text, name) {
			if mentionBoundary(text, loc[0], loc[1]) && !withinObjects(q.Annotation.Objects, loc[0], loc[1]) {
				mentions = append(mentions, mention{name, loc[0], loc[1]})
			}
		}
	}
	sort.Slice(mentions, func(i, j int) bool {
		if mentions[i].start != mentions[j].start {
			return mentions[i].start < mentions[j].start
		}
		return mentions[i].end > mentions[j].end
	})
	end := -1
	for _, m := range mentions {
		if m.start < end {
			continue
		}
		resolveName(&q, text[m.start:m.end], aliases[m.name], false)
		if len(aliases[m.name]) == 1 {
			for id := range aliases[m.name] {
				q.resolvedObjects = append(q.resolvedObjects, Object{ID: id, Name: text[m.start:m.end], Kind: "service", Span: sourceSpan(text, m.start, m.end, "resolved-explicit-alias")})
			}
		}
		end = m.end
	}
	q.ResolvedIDs = sortedUnique(q.ResolvedIDs)
	q.Ambiguous = sortedUnique(q.Ambiguous)
	q.Unknown = sortedUnique(q.Unknown)
	return q
}

// Canonical supplies resolved object mentions for applicability checks. It never
// converts unknown or ambiguous mentions into an asserted identity. Persist the
// raw Extract result instead; current alias provenance is resolved afresh.
func Canonical(q Query) Annotation {
	a := q.Annotation
	a.Objects = append([]Object{}, q.resolvedObjects...)
	a.Topics = append([]Topic{}, q.Annotation.Topics...)
	if len(a.Objects) == 0 {
		for _, o := range q.Annotation.Objects {
			if contains(q.ResolvedIDs, o.ID) {
				a.Objects = append(a.Objects, o)
			}
		}
	}
	sort.Slice(a.Objects, func(i, j int) bool { return a.Objects[i].Span.Start < a.Objects[j].Span.Start })
	bindTopics(&a, q.text)
	return a
}

// Compatible is an applicability filter, not a relevance score. An explicit
// but unresolved service cannot cause other service facts to be admitted.
func Compatible(q Query, m Annotation) (bool, string) {
	if len(q.ResolvedIDs)+len(q.Unknown)+len(q.Ambiguous) > 0 {
		relevant := []string{}
		for _, o := range m.Objects {
			if contains(q.ResolvedIDs, o.ID) {
				relevant = append(relevant, o.ID)
			}
		}
		if len(relevant) == 0 {
			if len(q.ResolvedIDs) == 0 {
				return false, "unresolved_query_object"
			}
			if len(m.Objects) == 0 {
				return false, "unresolved_memory_object"
			}
			return false, "different_object"
		}
		query := Canonical(q)
		reason := "different_topic"
		// Evaluate each subject separately: a swapped pair of properties must
		// not satisfy a query merely because the sets of words are equal.
		for _, id := range sortedUnique(relevant) {
			qt, mt := boundTopicSet(query, []string{id}), boundTopicSet(m, []string{id})
			if len(q.Annotation.Topics) > 0 && len(qt) == 0 {
				reason = "unbound_query_property"
				continue
			}
			ok, why := compatibleTopics(qt, mt)
			if ok {
				return true, why
			}
			reason = why
		}
		return false, reason
	}
	return compatibleTopics(topicSet(q.Annotation), topicSet(m))
}

func compatibleTopics(qt, mt map[string]bool) (bool, string) {
	qTypes, mTypes := credentialTypes(qt), credentialTypes(mt)
	if len(qTypes) > 0 && !intersects(qTypes, mTypes) {
		return false, "different_credential_type"
	}
	if qt["physical.key"] && !mt["physical.key"] {
		return false, "different_key_domain"
	}
	if !qt["physical.key"] && hasCredentialTopic(qt) && mt["physical.key"] && !hasCredentialTopic(mt) {
		return false, "different_key_domain"
	}
	var requested, candidate []string
	for _, key := range []string{"database", "port", "timeout", "owner", "credential.rotation", "physical.key"} {
		if qt[key] {
			requested = append(requested, key)
		}
		if mt[key] {
			candidate = append(candidate, key)
		}
	}
	if len(requested) > 0 && !intersects(requested, candidate) {
		return false, "different_topic"
	}
	if qt["credential.rotation.period"] && !mt["credential.rotation.period"] {
		return false, "missing_rotation_period"
	}
	return true, "compatible"
}

func Keys(a Annotation) []string {
	keys := []string{}
	for _, o := range a.Objects {
		keys = append(keys, "object:"+o.ID)
	}
	for _, t := range a.Topics {
		keys = append(keys, "topic:"+t.Key)
	}
	return sortedUnique(keys)
}

func QueryKeys(q Query) []string {
	keys := []string{}
	for _, id := range q.ResolvedIDs {
		keys = append(keys, "object:"+id)
	}
	for _, t := range q.Annotation.Topics {
		keys = append(keys, "topic:"+t.Key)
	}
	return sortedUnique(keys)
}

func sourceSpan(text string, start, end int, method string) Span {
	return Span{Text: text[start:end], Start: start, End: end, Method: method}
}

func canonical(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

func objectID(scope, kind, name string) string {
	h := sha256.Sum256([]byte(scope + "\x00" + kind + "\x00" + canonical(name)))
	return kind + "-" + hex.EncodeToString(h[:16])
}

func hasObject(objects []Object, id string, start int) bool {
	for _, o := range objects {
		if o.ID == id && o.Span.Start == start {
			return true
		}
	}
	return false
}

// Prefixes are grammatical boundaries, not a list of business object names.
// A compound name such as 售后订单服务 remains whole.
var servicePrefixes = []string{"请帮我比较", "请帮我查询", "请问一下", "我想知道", "请比较", "请查询", "请介绍", "请检查", "请问", "比较", "查询", "检查", "介绍", "关于", "对于", "以及", "还有", "和", "与", "及"}

func trimServicePrefix(text string, start, end int) int {
	for {
		changed := false
		for _, prefix := range servicePrefixes {
			if isConjunction(prefix) && !precededByService(text, start) {
				continue
			}
			if strings.HasPrefix(text[start:end], prefix) && len(text[start:end]) > len(prefix)+len("服务") {
				start += len(prefix)
				changed = true
				break
			}
		}
		if !changed {
			return start
		}
	}
}

func validService(name string) bool {
	base := strings.TrimSuffix(name, "服务")
	if utf8.RuneCountInString(base) < 1 || utf8.RuneCountInString(base) > 32 {
		return false
	}
	switch base {
	case "本", "该", "这个", "那个", "所有", "各", "各个", "哪些", "什么", "一个", "多个", "相关", "其他", "其它", "任何", "微", "的":
		return false
	}
	// Do not turn arbitrary prose or a negated relation into a name.
	for _, word := range []string{"不是", "并非", "不再", "不要", "不能", "的", "使用", "采用", "数据库", "简称", "别名", "是否"} {
		if strings.Contains(base, word) {
			return false
		}
	}
	return true
}

func negativeAliasClause(text string, start, end int) bool {
	left, right := start, end
	for left > 0 {
		r, size := utf8.DecodeLastRuneInString(text[:left])
		if strings.ContainsRune("。！？!?；;\n", r) {
			break
		}
		left -= size
	}
	for right < len(text) {
		r, size := utf8.DecodeRuneInString(text[right:])
		if strings.ContainsRune("。！？!?；;\n", r) {
			break
		}
		right += size
	}
	clause := text[left:right]
	for _, word := range []string{"不是", "并非", "不叫", "不再", "不是别名", "错误", "错误说法", "撤销", "取消别名"} {
		if strings.Contains(clause, word) {
			return true
		}
	}
	return false
}

func resolveName(q *Query, name string, ids map[string]bool, unknown bool) {
	if len(ids) > 1 {
		q.Ambiguous = append(q.Ambiguous, name)
		return
	}
	if len(ids) == 0 {
		if unknown {
			q.Unknown = append(q.Unknown, name)
		}
		return
	}
	for id := range ids {
		q.ResolvedIDs = append(q.ResolvedIDs, id)
	}
}

func copySet(in map[string]bool) map[string]bool {
	out := map[string]bool{}
	for key := range in {
		out[key] = true
	}
	return out
}

func literalLocations(text, literal string) [][2]int {
	// Go's Unicode case folding can change byte widths. ASCII aliases alone
	// use case folding; Chinese literals remain byte-exact for source offsets.
	pattern := regexp.QuoteMeta(literal)
	if asciiOnly(literal) {
		pattern = "(?i)" + pattern
	}
	var result [][2]int
	for _, loc := range regexp.MustCompile(pattern).FindAllStringIndex(text, -1) {
		result = append(result, [2]int{loc[0], loc[1]})
	}
	return result
}

func asciiOnly(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}
func previousRune(text string, pos int) rune {
	r, _ := utf8.DecodeLastRuneInString(text[:pos])
	return r
}
func isNameRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-'
}

func mentionBoundary(text string, start, end int) bool {
	if start > 0 && isNameRune(previousRune(text, start)) {
		// A Chinese question prefix may precede an alias without whitespace.
		left := start
		for left > 0 && isNameRune(previousRune(text, left)) {
			_, size := utf8.DecodeLastRuneInString(text[:left])
			left -= size
		}
		if !onlyGrammarPrefix(text[left:start]) {
			return false
		}
	}
	if end < len(text) {
		r, _ := utf8.DecodeRuneInString(text[end:])
		if isNameRune(r) && !strings.HasPrefix(text[end:], "的") {
			return false
		}
	}
	return true
}

func withinObjects(objects []Object, start, end int) bool {
	for _, o := range objects {
		if start >= o.Span.Start && end <= o.Span.End {
			return true
		}
	}
	return false
}

func extractTopics(a *Annotation, text string, start, end int) {
	clause := text[start:end]
	add := func(key string, from, to int) {
		a.Topics = append(a.Topics, Topic{Key: key, Span: sourceSpan(text, start+from, start+to, "bounded-topic-vocabulary")})
	}
	for _, property := range []struct {
		key   string
		terms []string
	}{
		{"database", []string{"数据库", "database", "databases"}},
		{"port", []string{"端口", "port", "ports"}},
		{"timeout", []string{"超时", "timeout", "timeouts"}},
		{"owner", []string{"负责人", "维护人", "owner", "maintainer"}},
	} {
		for _, loc := range matchTerms(clause, property.terms) {
			add(property.key, loc[0], loc[1])
		}
	}
	if loc := matchTerm(clause, []string{"钥匙", "physical key", "door key", "mechanical key"}); loc != nil {
		add("physical.key", loc[0], loc[1])
	}
	credential := false
	first, last := len(clause), 0
	for _, entry := range []struct {
		kind  string
		terms []string
	}{
		{"key", []string{"密钥", "api key", "secret key", "encryption key", "signing key", "access key"}},
		{"password", []string{"密码", "password", "passwords"}},
		{"token", []string{"令牌", "token", "tokens"}},
		{"certificate", []string{"证书", "certificate", "certificates"}},
		{"", []string{"凭据", "credential", "credentials"}},
	} {
		if loc := matchTerm(clause, entry.terms); loc != nil {
			credential = true
			if loc[0] < first {
				first = loc[0]
			}
			if loc[1] > last {
				last = loc[1]
			}
			if entry.kind != "" {
				add("credential.type."+entry.kind, loc[0], loc[1])
			}
		}
	}
	if rotation := rotationPattern.FindStringIndex(clause); credential && rotation != nil {
		if rotation[0] < first {
			first = rotation[0]
		}
		if rotation[1] > last {
			last = rotation[1]
		}
		add("credential.rotation", first, last)
		if period := periodPattern.FindStringIndex(clause); period != nil {
			if period[0] < first {
				first = period[0]
			}
			if period[1] > last {
				last = period[1]
			}
			add("credential.rotation.period", first, last)
		}
	}
}

func matchTerm(text string, terms []string) []int {
	matches := matchTerms(text, terms)
	if len(matches) == 0 {
		return nil
	}
	return matches[0]
}

func matchTerms(text string, terms []string) [][]int {
	result := [][]int{}
	for _, term := range terms {
		for _, loc := range literalLocations(text, term) {
			if asciiOnly(term) && ((loc[0] > 0 && isASCIINameRune(previousRune(text, loc[0]))) || (loc[1] < len(text) && isASCIINameRune(nextRune(text, loc[1])))) {
				continue
			}
			result = append(result, []int{loc[0], loc[1]})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i][0] != result[j][0] {
			return result[i][0] < result[j][0]
		}
		return result[i][1] > result[j][1]
	})
	return result
}

func isASCIINameRune(r rune) bool { return r < 128 && isNameRune(r) }
func isConjunction(s string) bool {
	return s == "和" || s == "与" || s == "及" || s == "以及" || s == "还有"
}
func precededByService(text string, start int) bool {
	return strings.HasSuffix(text[:start], "服务")
}

func topicSet(a Annotation) map[string]bool {
	result := map[string]bool{}
	for _, t := range a.Topics {
		result[t.Key] = true
	}
	return result
}
func credentialTypes(topics map[string]bool) []string {
	result := []string{}
	for t := range topics {
		if strings.HasPrefix(t, "credential.type.") {
			result = append(result, t)
		}
	}
	return result
}
func hasCredentialTopic(topics map[string]bool) bool {
	for t := range topics {
		if strings.HasPrefix(t, "credential.") {
			return true
		}
	}
	return false
}
func intersects(a, b []string) bool {
	for _, v := range a {
		if contains(b, v) {
			return true
		}
	}
	return false
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func sortedUnique(values []string) []string {
	sort.Strings(values)
	out := []string{}
	for _, v := range values {
		if len(out) == 0 || out[len(out)-1] != v {
			out = append(out, v)
		}
	}
	return out
}

func nextRune(text string, pos int) rune { r, _ := utf8.DecodeRuneInString(text[pos:]); return r }
func insideAlias(aliases []Alias, start, end int) bool {
	for _, a := range aliases {
		if start >= a.Span.Start && end <= a.Span.End {
			return true
		}
	}
	return false
}

// bindTopics supports the explicit subject-before-property form. It does not
// infer subject inheritance across sentences. Explicit adjacent conjunctions
// share the following property; other relation grammar stays unsupported.
func bindTopics(a *Annotation, text string) {
	for i := range a.Topics {
		t := &a.Topics[i]
		t.ObjectIDs = nil
		previous := -1
		for j, o := range a.Objects {
			if o.Span.End <= t.Span.Start && (previous < 0 || o.Span.Start > a.Objects[previous].Span.Start) {
				previous = j
			}
		}
		if previous < 0 {
			continue
		}
		o := a.Objects[previous]
		if o.Span.End > len(text) || t.Span.Start > len(text) || o.Span.End > t.Span.Start {
			continue
		}
		between := text[o.Span.End:t.Span.Start]
		if strings.ContainsAny(between, "。！？!?；;\n") {
			continue
		}
		t.ObjectIDs = []string{o.ID}
		// A bounded list of explicit subjects may share one following property.
		for previous > 0 {
			before := a.Objects[previous-1]
			if before.Span.End > o.Span.Start || o.Span.Start > len(text) {
				break
			}
			gap := strings.TrimSpace(text[before.Span.End:o.Span.Start])
			if gap != "和" && gap != "与" && gap != "及" && gap != "、" && strings.ToLower(gap) != "and" {
				break
			}
			t.ObjectIDs = append(t.ObjectIDs, before.ID)
			previous--
			o = before
		}
		t.ObjectIDs = sortedUnique(t.ObjectIDs)
	}
}

func onlyGrammarPrefix(prefix string) bool {
	for prefix != "" {
		changed := false
		for _, p := range servicePrefixes {
			if strings.HasPrefix(prefix, p) {
				prefix = strings.TrimPrefix(prefix, p)
				changed = true
				break
			}
		}
		if !changed {
			return false
		}
	}
	return true
}

func boundTopicSet(a Annotation, ids []string) map[string]bool {
	result := map[string]bool{}
	for _, topic := range a.Topics {
		if intersects(ids, topic.ObjectIDs) {
			result[topic.Key] = true
		}
	}
	return result
}
