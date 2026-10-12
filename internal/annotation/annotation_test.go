package annotation

import (
	"reflect"
	"strings"
	"testing"
)

func TestSourceSpansAndStableScopedIdentities(t *testing.T) {
	text := "请问历史订单服务（简称历史交易）的数据库和端口是什么？"
	a := Extract("project-a", text)
	if a.Version != Version || len(a.Objects) != 1 || a.Objects[0].Name != "历史订单服务" {
		t.Fatalf("unexpected object extraction: %+v", a)
	}
	if len(a.Aliases) != 1 || a.Aliases[0].Name != "历史交易" || a.Aliases[0].TargetID != a.Objects[0].ID {
		t.Fatalf("unexpected aliases: %+v", a.Aliases)
	}
	for _, span := range allSpans(a) {
		if span.Start < 0 || span.End > len(text) || text[span.Start:span.End] != span.Text || span.Method == "" {
			t.Fatalf("invalid original byte span: %+v", span)
		}
	}
	b := Extract("project-a", "历史订单服务的端口为 8080。")
	c := Extract("project-b", "历史订单服务的端口为 8080。")
	if a.Objects[0].ID != b.Objects[0].ID || a.Objects[0].ID == c.Objects[0].ID {
		t.Fatal("object identity must be stable within a scope and different across scopes")
	}
	if len(a.Topics) != 2 || !reflect.DeepEqual(a.Topics[0].ObjectIDs, []string{a.Objects[0].ID}) {
		t.Fatalf("property must retain its explicit subject: %+v", a.Topics)
	}
}

func TestExplicitServicesAliasesAndUnknowns(t *testing.T) {
	orders := Extract("a", "订单服务（简称交易）的数据库为 PostgreSQL。")
	billing := Extract("a", "账单服务（别名结算）的数据库为 MySQL。")
	history := Extract("a", "历史订单服务的数据库为 SQLite。")
	registry := []Annotation{orders, billing, history}
	for _, tc := range []struct {
		text    string
		ids     []string
		unknown []string
	}{
		{"订单服务的数据库", []string{orders.Objects[0].ID}, []string{}},
		{"交易的数据库", []string{orders.Objects[0].ID}, []string{}},
		{"请问交易的数据库", []string{orders.Objects[0].ID}, []string{}},
		{"比较订单服务和账单服务的数据库", sortedUnique([]string{orders.Objects[0].ID, billing.Objects[0].ID}), []string{}},
		{"历史订单服务的数据库", []string{history.Objects[0].ID}, []string{}},
		{"库存服务的数据库", []string{}, []string{"库存服务"}},
		{"交易记录服务的数据库", []string{}, []string{"交易记录服务"}},
		{"各服务的数据库", []string{}, []string{}},
		{"交易记录的数据库", []string{}, []string{}},
	} {
		t.Run(tc.text, func(t *testing.T) {
			q := Resolve("a", tc.text, registry)
			if !reflect.DeepEqual(q.ResolvedIDs, tc.ids) || !reflect.DeepEqual(q.Unknown, tc.unknown) || len(q.Ambiguous) != 0 {
				t.Fatalf("unexpected query: %+v", q)
			}
		})
	}
	for _, text := range []string{"订单服务的数据库", "交易的数据库"} {
		q := Resolve("a", text, registry)
		if ok, reason := Compatible(q, Canonical(Resolve("a", "订单服务的数据库为 PostgreSQL。", registry))); !ok {
			t.Fatal(reason)
		}
		if ok, _ := Compatible(q, billing); ok {
			t.Fatal("other service admitted")
		}
	}
	unknown := Resolve("a", "库存服务的数据库", registry)
	if ok, _ := Compatible(unknown, orders); ok {
		t.Fatal("unknown service replaced with known service")
	}
	if ok, _ := Compatible(unknown, Extract("a", "这个项目数据库为 SQLite。")); ok {
		t.Fatal("unknown service admitted a generic project fact")
	}
}

func TestAliasCollisionAndScopeDoNotGuess(t *testing.T) {
	a := Extract("a", "支付服务（简称核心）的数据库为 PostgreSQL。")
	b := Extract("a", "发票服务（别名：核心）的数据库为 MySQL。")
	q := Resolve("a", "核心 数据库", []Annotation{a, b})
	if len(q.ResolvedIDs) != 0 || !reflect.DeepEqual(q.Ambiguous, []string{"核心"}) {
		t.Fatalf("expected alias collision: %+v", q)
	}
	if ok, _ := Compatible(q, a); ok {
		t.Fatal("ambiguous alias admitted")
	}
	if len(Canonical(q).Objects) != 0 {
		t.Fatal("ambiguous alias canonicalized")
	}
	foreign := Extract("b", "发票服务（简称核心）的数据库为 MySQL。")
	q = Resolve("a", "核心 数据库", []Annotation{a, foreign})
	if !reflect.DeepEqual(q.ResolvedIDs, []string{a.Objects[0].ID}) || len(q.Ambiguous) != 0 {
		t.Fatalf("foreign registry contaminated scope: %+v", q)
	}
	q = Resolve("a", "发票服务 数据库", []Annotation{foreign})
	if len(q.ResolvedIDs) != 0 || len(q.Unknown) != 1 {
		t.Fatalf("foreign object resolved: %+v", q)
	}
}

func TestAliasMustHavePositiveSourceDeclaration(t *testing.T) {
	for _, text := range []string{
		"支付服务不叫核心。",
		"支付服务（不是简称核心）的数据库为 PostgreSQL。",
		"支付服务（简称核心）是错误说法。",
		"撤销支付服务（简称核心）这个称呼。",
		"支付服务的别名可能是核心。",
	} {
		if a := Extract("a", text); len(a.Aliases) != 0 {
			t.Fatalf("negative/unsupported declaration acquired alias: %s %+v", text, a)
		}
	}
	a := Extract("a", "支付服务（别名：核心服务）的数据库为 PostgreSQL。")
	if len(a.Objects) != 1 || len(a.Aliases) != 1 {
		t.Fatalf("alias declaration must not create second independent identity: %+v", a)
	}
	q := Resolve("a", "核心服务的数据库", []Annotation{a})
	if len(q.Ambiguous) != 0 || !reflect.DeepEqual(q.ResolvedIDs, []string{a.Objects[0].ID}) {
		t.Fatalf("service-shaped alias unresolved: %+v", q)
	}
}

func TestCanonicalAliasFactsAndPropertyBinding(t *testing.T) {
	declaration := Extract("a", "支付服务（简称结算）的端口为 8080。")
	registry := []Annotation{declaration}
	q := Resolve("a", "支付服务的数据库", registry)
	aliasText := "结算的数据库为 PostgreSQL。"
	raw := Extract("a", aliasText)
	if len(raw.Objects) != 0 {
		t.Fatal("alias identity was invented without registry")
	}
	m := Canonical(Resolve("a", aliasText, registry))
	if len(m.Objects) != 1 || m.Objects[0].Span.Text != "结算" || m.Objects[0].Span.Method != "resolved-explicit-alias" {
		t.Fatalf("resolved source mention lost: %+v", m)
	}
	if ok, reason := Compatible(q, m); !ok {
		t.Fatalf("alias fact rejected: %s %+v", reason, m)
	}
	if ok, _ := Compatible(q, declaration); ok {
		t.Fatal("same service's port admitted for database query")
	}
	if ok, _ := Compatible(q, Canonical(Resolve("a", aliasText, nil))); ok {
		t.Fatal("withdrawn alias identity retained")
	}
	for _, property := range []string{"端口", "超时", "负责人"} {
		if ok, _ := Compatible(q, Canonical(Resolve("a", "支付服务的"+property+"已经确定。", registry))); ok {
			t.Fatalf("different property %s admitted", property)
		}
	}
	other := Extract("a", "通知服务的数据库为 MySQL。")
	registry = append(registry, other)
	m = Canonical(Resolve("a", "支付服务的端口是 8080，通知服务的数据库使用 MySQL。", registry))
	if ok, _ := Compatible(q, m); ok {
		t.Fatal("other service's property was bound to requested service")
	}
}

func TestCredentialRotationVocabularyKeepsDomainsAndProperties(t *testing.T) {
	for _, tc := range []struct {
		query, memory string
		want          bool
	}{
		{"凭据的轮换周期", "密钥每九十天更换一次。", true},
		{"凭据的轮换周期", "密码每三十天更换一次。", true},
		{"密钥的轮换周期", "密码每三十天更换一次。", false},
		{"密钥的轮换周期", "门锁钥匙每三年更换一次。", false},
		{"门锁钥匙的更换周期", "密钥每九十天更换一次。", false},
		{"门锁钥匙的更换周期", "门锁钥匙每三年更换一次。", true},
		{"凭据的轮换周期", "密钥使用 Base64 编码。", false},
		{"凭据的轮换周期", "密钥定期更换。", false},
		{"token rotation interval", "API keys rotate every 90 days.", false},
		{"credential rotation interval", "API key rotates every 90 days.", true},
		{"凭据的轮换周期", "密钥用于认证。门锁钥匙每年更换。", false},
	} {
		t.Run(tc.query+"/"+tc.memory, func(t *testing.T) {
			q := Resolve("a", tc.query, nil)
			m := Extract("a", tc.memory)
			got, reason := Compatible(q, m)
			if got != tc.want {
				t.Fatalf("got %v want %v reason %s q=%+v m=%+v", got, tc.want, reason, q, m)
			}
		})
	}
}

func TestKeysAndLatinServiceNames(t *testing.T) {
	a := Extract("a", "The ledger-service database uses PostgreSQL.")
	if len(a.Objects) != 1 || a.Objects[0].Name != "ledger-service" {
		t.Fatalf("unexpected Latin service: %+v", a)
	}
	q := Resolve("a", "LEDGER-SERVICE database", []Annotation{a})
	if !reflect.DeepEqual(QueryKeys(q), Keys(a)) {
		t.Fatalf("query and write keys differ: %v %v", QueryKeys(q), Keys(a))
	}
	for _, key := range Keys(a) {
		if !strings.HasPrefix(key, "object:") && !strings.HasPrefix(key, "topic:") {
			t.Fatalf("bad key %s", key)
		}
	}
	if other := Extract("a", "ledger-services-old database"); len(other.Objects) != 0 {
		t.Fatalf("substring service recognized: %+v", other)
	}
}

func TestCompoundNamesAreNotConjunctions(t *testing.T) {
	for _, name := range []string{"和谐服务", "与众服务", "在途服务", "把关服务", "及时服务", "历史订单服务"} {
		a := Extract("a", name+"的数据库为 SQLite。")
		if len(a.Objects) != 1 || a.Objects[0].Name != name {
			t.Fatalf("compound name was shortened: %s %+v", name, a)
		}
	}
	a := Extract("a", "订单服务和账单服务的数据库")
	if len(a.Objects) != 2 || a.Objects[0].Name != "订单服务" || a.Objects[1].Name != "账单服务" {
		t.Fatalf("service conjunction not separated: %+v", a)
	}
}

func TestRepeatedPropertiesKeepTheirOwnSubjects(t *testing.T) {
	text := "支付服务的数据库为 PostgreSQL，通知服务的数据库为 MySQL。"
	a := Extract("a", text)
	if len(a.Topics) != 2 || len(a.Objects) != 2 {
		t.Fatalf("repeated property lost: %+v", a)
	}
	for i, topic := range a.Topics {
		if !reflect.DeepEqual(topic.ObjectIDs, []string{a.Objects[i].ID}) {
			t.Fatalf("property bound to wrong service: %+v", a)
		}
	}
	for _, name := range []string{"支付服务", "通知服务"} {
		q := Resolve("a", name+"的数据库", []Annotation{a})
		if ok, reason := Compatible(q, a); !ok {
			t.Fatalf("own subject property missing: %s %s", name, reason)
		}
	}
	q := Resolve("a", "凭据的轮换周期", nil)
	if ok, _ := Compatible(q, Extract("a", "密钥用于认证，门锁钥匙每年更换。")); ok {
		t.Fatal("credential and physical replacement clauses were combined")
	}
}

func TestQueryPropertiesStayBoundToTheirNamedSubjects(t *testing.T) {
	registry := []Annotation{Extract("a", "支付服务的数据库为 PostgreSQL。"), Extract("a", "通知服务的端口为 8080。")}
	for _, tc := range []struct {
		query, memory string
		want          bool
	}{
		{"支付服务的数据库，通知服务的端口", "支付服务的端口为 9090。", false},
		{"支付服务的数据库，通知服务的端口", "通知服务的数据库为 MySQL。", false},
		{"支付服务的数据库，通知服务的端口", "支付服务的数据库为 PostgreSQL。", true},
		{"支付服务的数据库，通知服务的端口", "通知服务的端口为 8080。", true},
		{"支付服务的数据库，通知服务的端口", "支付服务的端口为 9090，通知服务的数据库为 MySQL。", false},
		{"支付服务和通知服务的数据库", "支付服务的端口为 9090。", false},
		{"支付服务和通知服务的数据库", "支付服务的数据库为 PostgreSQL。", true},
		{"支付服务和通知服务的数据库", "通知服务的数据库为 MySQL。", true},
	} {
		q := Resolve("a", tc.query, registry)
		m := Canonical(Resolve("a", tc.memory, registry))
		if got, reason := Compatible(q, m); got != tc.want {
			t.Fatalf("%s / %s: got %v (%s)", tc.query, tc.memory, got, reason)
		}
	}
}

func allSpans(a Annotation) []Span {
	var spans []Span
	for _, o := range a.Objects {
		spans = append(spans, o.Span)
	}
	for _, v := range a.Aliases {
		spans = append(spans, v.Span)
	}
	for _, v := range a.Topics {
		spans = append(spans, v.Span)
	}
	return spans
}
