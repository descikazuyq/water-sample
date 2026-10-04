package water

import (
	"math"
	"testing"
)

// 数值比较边界的回归保障：测量值严格大于采样当时适用的上限才超标，
// 等于或小于都达标。登记限值和录入测量只要求数值有限，零和负数都是
// 合法数值。判定依据实际录入的 float64 数值，不做四舍五入，也不设
// 允许误差；确认保存的逐项结果保留原测量值、实际采用的上限及其生效时间。

// resultOf 在确认返回或查询到的样品里按项目取逐项结果。
func resultOf(t *testing.T, smp Sample, item string) ItemResult {
	t.Helper()
	for _, r := range smp.Results {
		if r.Item == item {
			return r
		}
	}
	t.Fatalf("sample %s has no result for item %q: %+v", smp.ID, item, smp.Results)
	return ItemResult{}
}

// findInList 按编号从按点查询结果里取样品。
func findInList(t *testing.T, list []Sample, id string) Sample {
	t.Helper()
	for _, smp := range list {
		if smp.ID == id {
			return smp
		}
	}
	t.Fatalf("sample %q missing from list %+v", id, list)
	return Sample{}
}

// 上限附近：恰好相等、float64 仍能明确区分的较小值与较大值。
// 三个测量值按常见小数位数（如 %.2f）显示都是 8.00，但判定必须依据
// 实际录入的数值：较小值与相等值达标，较大值超标。
func TestConfirmStrictlyGreaterBoundary(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	// 采样时间 9/5 落在两版限值之间：应使用 9/1 生效的 8.0，
	// 沿用按采样时间选择适用限值的规则，9/10 才生效的 9.0 不参与本次判定。
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "pH", 9.0, at(10, 0))

	below := math.Nextafter(8.0, 0)  // 比 8.0 小的相邻 float64
	above := math.Nextafter(8.0, 10) // 比 8.0 大的相邻 float64
	cases := []struct {
		id       string
		value    float64
		exceeded bool
	}{
		{"S-below", below, false},
		{"S-equal", 8.0, false},
		{"S-above", above, true},
	}
	for _, c := range cases {
		mustSample(t, s, c.id, "P1", at(5, 0), Measurement{Item: "pH", Value: c.value})
	}

	// check 核对确认返回与按点查询两处看到的同一份记录。
	check := func(smp Sample, value float64, exceeded bool, label string) {
		t.Helper()
		if smp.Status != StatusConfirmed {
			t.Fatalf("%s: 应为已确认，实际 %s: %+v", label, smp.Status, smp)
		}
		if smp.Exceeded != exceeded {
			t.Fatalf("%s: 整份超标标记应为 %v: %+v", label, exceeded, smp)
		}
		r := resultOf(t, smp, "pH")
		// 保存的必须是原测量值本身：不能把非常接近上限的测量值
		// 四舍五入或钳制成上限 8.0。
		if r.Value != value {
			t.Fatalf("%s: 保存的测量值应为原录入值 %v，实际 %v", label, value, r.Value)
		}
		if r.Limit != 8.0 || !r.LimitEffective.Equal(at(1, 0)) {
			t.Fatalf("%s: 应采用 9/1 生效的上限 8.0，实际 %+v", label, r)
		}
		if r.Exceeded != exceeded {
			t.Fatalf("%s: 单项超标标记应为 %v: %+v", label, exceeded, r)
		}
	}

	for _, c := range cases {
		confirmed, err := s.Confirm(c.id)
		if err != nil {
			t.Fatalf("Confirm(%s): %v", c.id, err)
		}
		check(confirmed, c.value, c.exceeded, c.id+" 确认返回")
	}
	list, err := s.ListByPoint("P1")
	if err != nil {
		t.Fatalf("ListByPoint: %v", err)
	}
	for _, c := range cases {
		check(findInList(t, list, c.id), c.value, c.exceeded, c.id+" 按点查询")
	}

	// 重复确认返回已保存结果；关闭后重新打开，判定依据保持原样。
	again, err := s.Confirm("S-above")
	if err != nil {
		t.Fatalf("re-confirm: %v", err)
	}
	check(again, above, true, "S-above 重复确认")
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
		check(findInList(t, list2, c.id), c.value, c.exceeded, c.id+" 重开后按点查询")
	}
}

// 零上限是真实的适用限值，不能当成缺少限值；负上限按原始数值的
// 大小关系判定，不取绝对值，也不把合法负数改成零后再判定。
func TestConfirmZeroAndNegativeLimits(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "zero", 0.0, at(1, 0))
	mustLimit(t, s, "P1", "neg", -2.0, at(1, 0))

	cases := []struct {
		id       string
		item     string
		value    float64
		limit    float64
		exceeded bool
	}{
		// 零上限：零达标，有限的微小正数超标，负数达标
		{"S-zero-eq", "zero", 0.0, 0.0, false},
		{"S-zero-tiny", "zero", math.SmallestNonzeroFloat64, 0.0, true},
		{"S-zero-neg", "zero", -1e-300, 0.0, false},
		// 负上限：-3、-2 达标，-1 超标
		{"S-neg-below", "neg", -3.0, -2.0, false},
		{"S-neg-eq", "neg", -2.0, -2.0, false},
		{"S-neg-above", "neg", -1.0, -2.0, true},
	}
	for _, c := range cases {
		mustSample(t, s, c.id, "P1", at(5, 0), Measurement{Item: c.item, Value: c.value})
		confirmed, err := s.Confirm(c.id)
		if err != nil {
			t.Fatalf("Confirm(%s): %v", c.id, err)
		}
		if confirmed.Status != StatusConfirmed || confirmed.Exceeded != c.exceeded {
			t.Fatalf("%s: 状态或整份结论错误: %+v", c.id, confirmed)
		}
		r := resultOf(t, confirmed, c.item)
		if r.Value != c.value || r.Limit != c.limit || !r.LimitEffective.Equal(at(1, 0)) {
			t.Fatalf("%s: 保存的判定依据应为原测量值 %v 与上限 %v，实际 %+v", c.id, c.value, c.limit, r)
		}
		if r.Exceeded != c.exceeded {
			t.Fatalf("%s: 单项超标标记应为 %v: %+v", c.id, c.exceeded, r)
		}
	}

	// 按采样点查看同一份样品，数值与判定依据与确认返回一致。
	list, err := s.ListByPoint("P1")
	if err != nil {
		t.Fatalf("ListByPoint: %v", err)
	}
	for _, c := range cases {
		smp := findInList(t, list, c.id)
		r := resultOf(t, smp, c.item)
		if smp.Exceeded != c.exceeded || r.Value != c.value || r.Limit != c.limit || r.Exceeded != c.exceeded {
			t.Fatalf("%s: 按点查询与确认返回不一致: %+v", c.id, smp)
		}
	}
}

// 同一份样品含两个项目：各自使用本项目的限值。一个项目仅略高于上限、
// 另一个恰好等于上限时，前者超标、后者达标，整份超标；所有项目都不大于
// 各自上限时整份才达标。结果应能对应回原测量。
func TestConfirmMultiItemBoundary(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))

	phAbove := math.Nextafter(8.0, 10)  // 仅略高于 pH 上限
	phBelow := math.Nextafter(8.0, 0)   // 仅略低于 pH 上限
	codBelow := math.Nextafter(30.0, 0) // 仅略低于 COD 上限

	// 一项仅略高于上限、一项恰好等于上限：整份超标
	mustSample(t, s, "S-mix", "P1", at(5, 0),
		Measurement{Item: "pH", Value: phAbove},
		Measurement{Item: "COD", Value: 30.0})
	// 两项都不大于各自上限：整份达标
	mustSample(t, s, "S-pass", "P1", at(6, 0),
		Measurement{Item: "pH", Value: phBelow},
		Measurement{Item: "COD", Value: codBelow})

	mix, err := s.Confirm("S-mix")
	if err != nil {
		t.Fatalf("Confirm S-mix: %v", err)
	}
	if mix.Status != StatusConfirmed || !mix.Exceeded {
		t.Fatalf("S-mix 应为已确认且整份超标: %+v", mix)
	}
	if r := resultOf(t, mix, "pH"); r.Value != phAbove || r.Limit != 8.0 || !r.Exceeded {
		t.Fatalf("S-mix pH 应超标且保留原测量值 %v: %+v", phAbove, r)
	}
	if r := resultOf(t, mix, "COD"); r.Value != 30.0 || r.Limit != 30.0 || r.Exceeded {
		t.Fatalf("S-mix COD 恰好等于上限应达标: %+v", r)
	}

	pass, err := s.Confirm("S-pass")
	if err != nil {
		t.Fatalf("Confirm S-pass: %v", err)
	}
	if pass.Status != StatusConfirmed || pass.Exceeded {
		t.Fatalf("S-pass 应为已确认且整份达标: %+v", pass)
	}
	if r := resultOf(t, pass, "pH"); r.Value != phBelow || r.Limit != 8.0 || r.Exceeded {
		t.Fatalf("S-pass pH 应达标且保留原测量值 %v: %+v", phBelow, r)
	}
	if r := resultOf(t, pass, "COD"); r.Value != codBelow || r.Limit != 30.0 || r.Exceeded {
		t.Fatalf("S-pass COD 应达标且保留原测量值 %v: %+v", codBelow, r)
	}

	// 按采样点查看：两份样品的逐项结果与确认返回一致，
	// 非常接近上限的测量值没有被保存成上限本身。
	list, err := s.ListByPoint("P1")
	if err != nil {
		t.Fatalf("ListByPoint: %v", err)
	}
	mixListed := findInList(t, list, "S-mix")
	if !mixListed.Exceeded {
		t.Fatalf("按点查询 S-mix 应整份超标: %+v", mixListed)
	}
	if r := resultOf(t, mixListed, "pH"); r.Value != phAbove || !r.Exceeded {
		t.Fatalf("按点查询 S-mix pH 与确认返回不一致: %+v", r)
	}
	if r := resultOf(t, mixListed, "COD"); r.Value != 30.0 || r.Exceeded {
		t.Fatalf("按点查询 S-mix COD 与确认返回不一致: %+v", r)
	}
	passListed := findInList(t, list, "S-pass")
	if passListed.Exceeded {
		t.Fatalf("按点查询 S-pass 应整份达标: %+v", passListed)
	}
	if r := resultOf(t, passListed, "pH"); r.Value != phBelow || r.Exceeded {
		t.Fatalf("按点查询 S-pass pH 与确认返回不一致: %+v", r)
	}
	if r := resultOf(t, passListed, "COD"); r.Value != codBelow || r.Exceeded {
		t.Fatalf("按点查询 S-pass COD 与确认返回不一致: %+v", r)
	}
}
