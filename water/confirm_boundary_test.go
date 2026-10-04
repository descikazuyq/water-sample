package water

import (
	"fmt"
	"math"
	"testing"
)

// 本文件为确认的数值比较边界补回归保障：测量值严格大于采样当时适用的上限
// 才超标，等于或小于上限都达标。登记限值和录入测量只要求数值有限，零和负数
// 同样是合法的限值与测量值。判定依据实际录入的 float64 数值，不先四舍五入，
// 也不设允许误差；确认保存的逐项结果保留原测量值、实际采用的上限及其生效
// 时间，按采样点查询看到的内容与确认返回的记录一致。
// 录入、确认、查询的既有行为不变，这里只通过现有公开功能核对结果。

// resultsByItem 把逐项结果按项目名索引，方便不按提交顺序核对。
func resultsByItem(rs []ItemResult) map[string]ItemResult {
	m := make(map[string]ItemResult, len(rs))
	for _, r := range rs {
		m[r.Item] = r
	}
	return m
}

// findSample 在按点查询结果里找指定编号的样品，找不到即失败。
func findSample(t *testing.T, list []Sample, id string) Sample {
	t.Helper()
	for _, smp := range list {
		if smp.ID == id {
			return smp
		}
	}
	t.Fatalf("sample %q missing from list %+v", id, list)
	return Sample{}
}

// 上限附近的三个 float64 值——恰好等于上限、比上限小一个最小步长、比上限大
// 一个最小步长——按常见小数位数显示时看起来一样，但结论必须按实际录入的
// 数值判定：较小值与相等值达标，较大值超标。确认返回、按点查询与重新打开
// 后看到的逐项结果都必须保留原测量值，不能把非常接近上限的测量值存成上限
// 本身；实际采用的上限沿用按采样时间选择适用版本的规则。
func TestConfirmNearLimitBoundary(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	// 采样之后才生效的新版上限不参与本次判定：采样当时适用的仍是 8.0@09-01。
	mustLimit(t, s, "P1", "pH", 100.0, at(20, 0))

	const limit = 8.0
	below := math.Nextafter(limit, math.Inf(-1)) // float64 中比 8.0 小的相邻值
	above := math.Nextafter(limit, math.Inf(1))  // float64 中比 8.0 大的相邻值
	// 前提：三个值在 float64 中明确可区分，但按两位小数显示时看起来一样。
	if below == limit || above == limit {
		t.Fatalf("test premise: boundary values must be distinct from the limit")
	}
	if fmt.Sprintf("%.2f", below) != "8.00" || fmt.Sprintf("%.2f", above) != "8.00" {
		t.Fatalf("test premise: boundary values should display as 8.00")
	}

	cases := []struct {
		id       string
		value    float64
		exceeded bool
	}{
		{"S-below", below, false}, // 比上限小一个最小步长：达标
		{"S-equal", limit, false}, // 恰好等于上限：达标
		{"S-above", above, true},  // 比上限大一个最小步长：超标
	}
	for _, c := range cases {
		mustSample(t, s, c.id, "P1", at(10, 0), Measurement{Item: "pH", Value: c.value})
	}

	// check 核对一份样品在确认返回、按点查询、重新打开后都应看到的内容：
	// 已确认，单项结果保留原测量值、实际采用的上限 8.0 及其生效时间 09-01，
	// 单项与整份超标标记与严格大于上限的比较结果一致。
	check := func(smp Sample, c struct {
		id       string
		value    float64
		exceeded bool
	}, label string) {
		t.Helper()
		if smp.Status != StatusConfirmed {
			t.Fatalf("%s: %s should be confirmed, got %s", label, c.id, smp.Status)
		}
		if smp.Exceeded != c.exceeded {
			t.Fatalf("%s: %s overall exceeded = %v, want %v", label, c.id, smp.Exceeded, c.exceeded)
		}
		if len(smp.Results) != 1 {
			t.Fatalf("%s: %s should have exactly one item result, got %+v", label, c.id, smp.Results)
		}
		r := smp.Results[0]
		if r.Item != "pH" || r.Value != c.value {
			t.Fatalf("%s: %s must keep the original measurement %v, got %+v", label, c.id, c.value, r)
		}
		if r.Limit != limit || !r.LimitEffective.Equal(at(1, 0)) {
			t.Fatalf("%s: %s should use limit 8.0 effective 09-01, got %+v", label, c.id, r)
		}
		if r.Exceeded != c.exceeded {
			t.Fatalf("%s: %s item exceeded = %v, want %v", label, c.id, r.Exceeded, c.exceeded)
		}
	}

	for _, c := range cases {
		smp, err := s.Confirm(c.id)
		if err != nil {
			t.Fatalf("Confirm(%s): %v", c.id, err)
		}
		check(smp, c, "confirm 返回")
	}

	// 按采样点查看同一份样品：数值与判定依据与确认返回的记录一致，
	// 特别是比上限大一个最小步长的测量值不能被保存成上限本身。
	list, err := s.ListByPoint("P1")
	if err != nil {
		t.Fatalf("ListByPoint: %v", err)
	}
	for _, c := range cases {
		check(findSample(t, list, c.id), c, "按点查询")
	}

	// 关闭后重新打开同一目录：边界测量值与判定依据原样保留。
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	list2, err := s2.ListByPoint("P1")
	if err != nil {
		t.Fatalf("reopened ListByPoint: %v", err)
	}
	for _, c := range cases {
		check(findSample(t, list2, c.id), c, "重开后按点查询")
	}
}

// 零上限是真实的适用限值，不能当成缺少限值；负上限同样按原始数值的大小
// 关系判定，不能取绝对值比较，也不能把合法的负数改成零后再判定。
func TestConfirmZeroAndNegativeLimits(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "zero", 0.0, at(1, 0)) // 零上限
	mustLimit(t, s, "P1", "neg", -2.0, at(1, 0)) // 负上限

	tiny := math.Nextafter(0, math.Inf(1)) // 有限的最小正数
	cases := []struct {
		id       string
		item     string
		value    float64
		limit    float64
		exceeded bool
	}{
		{"S-zero-eq", "zero", 0, 0, false},      // 测量值为零：达标
		{"S-zero-pos", "zero", tiny, 0, true},   // 微小正数大于零上限：超标
		{"S-zero-neg", "zero", -tiny, 0, false}, // 负数小于零上限：达标
		{"S-neg-below", "neg", -3, -2, false},   // -3 < -2：达标（取绝对值会判错）
		{"S-neg-eq", "neg", -2, -2, false},      // 恰好等于负上限：达标
		{"S-neg-above", "neg", -1, -2, true},    // -1 > -2：超标
	}
	for _, c := range cases {
		mustSample(t, s, c.id, "P1", at(10, 0), Measurement{Item: c.item, Value: c.value})
		smp, err := s.Confirm(c.id)
		if err != nil {
			// 零上限必须能找到适用限值，不能报缺少上限
			t.Fatalf("Confirm(%s): %v", c.id, err)
		}
		if smp.Status != StatusConfirmed || smp.Exceeded != c.exceeded {
			t.Fatalf("%s: confirmed = %s, exceeded = %v, want %v", c.id, smp.Status, smp.Exceeded, c.exceeded)
		}
		if len(smp.Results) != 1 {
			t.Fatalf("%s: expected one item result, got %+v", c.id, smp.Results)
		}
		r := smp.Results[0]
		if r.Item != c.item || r.Value != c.value || r.Limit != c.limit ||
			!r.LimitEffective.Equal(at(1, 0)) || r.Exceeded != c.exceeded {
			t.Fatalf("%s: result = %+v, want item %s value %v limit %v exceeded %v",
				c.id, r, c.item, c.value, c.limit, c.exceeded)
		}
	}

	// 按采样点查询看到的判定依据与确认返回一致：零与负数都按原数值保存。
	list, err := s.ListByPoint("P1")
	if err != nil {
		t.Fatalf("ListByPoint: %v", err)
	}
	for _, c := range cases {
		smp := findSample(t, list, c.id)
		if smp.Exceeded != c.exceeded || len(smp.Results) != 1 {
			t.Fatalf("list %s: %+v", c.id, smp)
		}
		r := smp.Results[0]
		if r.Value != c.value || r.Limit != c.limit || r.Exceeded != c.exceeded {
			t.Fatalf("list %s: result = %+v, want value %v limit %v exceeded %v",
				c.id, r, c.value, c.limit, c.exceeded)
		}
	}
}

// 同一份样品含两个项目时各自使用本项目的限值：一个项目仅略高于上限、
// 另一个恰好等于上限，前者超标、后者达标，整份样品仍超标；所有项目都
// 不大于各自上限时，整份才达标。结果应能对应回原测量。
func TestConfirmTwoItemsMixedBoundary(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))

	phAbove := math.Nextafter(8.0, math.Inf(1))    // 仅略高于 pH 上限
	codBelow := math.Nextafter(30.0, math.Inf(-1)) // 仅略低于 COD 上限

	// S1：pH 略超标、COD 恰好等于上限 → 整份超标
	mustSample(t, s, "S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: phAbove},
		Measurement{Item: "COD", Value: 30.0})
	// S2：pH 恰好等于上限、COD 略低于上限 → 整份达标
	mustSample(t, s, "S2", "P1", at(11, 0),
		Measurement{Item: "pH", Value: 8.0},
		Measurement{Item: "COD", Value: codBelow})

	s1, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("Confirm S1: %v", err)
	}
	if s1.Status != StatusConfirmed || !s1.Exceeded {
		t.Fatalf("S1: one item above its limit should make the whole sample exceeded, got %+v", s1)
	}
	rs1 := resultsByItem(s1.Results)
	if len(rs1) != 2 {
		t.Fatalf("S1: expected two item results, got %+v", s1.Results)
	}
	// 每个项目使用本项目的限值，结果对应回原测量值
	if ph := rs1["pH"]; ph.Value != phAbove || ph.Limit != 8.0 ||
		!ph.LimitEffective.Equal(at(1, 0)) || !ph.Exceeded {
		t.Fatalf("S1 pH: %+v, want value %v limit 8.0 exceeded", ph, phAbove)
	}
	if cod := rs1["COD"]; cod.Value != 30.0 || cod.Limit != 30.0 ||
		!cod.LimitEffective.Equal(at(1, 0)) || cod.Exceeded {
		t.Fatalf("S1 COD: %+v, want value 30 limit 30 not exceeded", cod)
	}

	s2, err := s.Confirm("S2")
	if err != nil {
		t.Fatalf("Confirm S2: %v", err)
	}
	if s2.Status != StatusConfirmed || s2.Exceeded {
		t.Fatalf("S2: all items within their limits should make the whole sample pass, got %+v", s2)
	}
	rs2 := resultsByItem(s2.Results)
	if len(rs2) != 2 {
		t.Fatalf("S2: expected two item results, got %+v", s2.Results)
	}
	if ph := rs2["pH"]; ph.Value != 8.0 || ph.Limit != 8.0 || ph.Exceeded {
		t.Fatalf("S2 pH: %+v, want value 8 limit 8 not exceeded", ph)
	}
	if cod := rs2["COD"]; cod.Value != codBelow || cod.Limit != 30.0 || cod.Exceeded {
		t.Fatalf("S2 COD: %+v, want value %v limit 30 not exceeded", cod, codBelow)
	}

	// 按采样点查询：两份样品的逐项依据与确认返回一致，整份结论不变。
	list, err := s.ListByPoint("P1")
	if err != nil {
		t.Fatalf("ListByPoint: %v", err)
	}
	l1 := findSample(t, list, "S1")
	if !l1.Exceeded || resultsByItem(l1.Results)["pH"].Value != phAbove ||
		!resultsByItem(l1.Results)["pH"].Exceeded || resultsByItem(l1.Results)["COD"].Exceeded {
		t.Fatalf("list S1 mismatch: %+v", l1)
	}
	l2 := findSample(t, list, "S2")
	if l2.Exceeded || resultsByItem(l2.Results)["COD"].Value != codBelow ||
		resultsByItem(l2.Results)["pH"].Exceeded || resultsByItem(l2.Results)["COD"].Exceeded {
		t.Fatalf("list S2 mismatch: %+v", l2)
	}
}
