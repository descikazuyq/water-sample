package water

import (
	"testing"
	"time"
)

// 本文件只给 LatestResult 的现有选择规则补回归保障：
// 结果必须来自该采样点“已确认且未作废”的样品，采样时刻最晚者胜，
// 时刻完全相同才按样品编号升序；录入先后、确认先后、是否超标都不改变次序。
// 列表排序与作废排除在别处已有覆盖，这里重点保护多份“已确认”样品
// 采样时刻相同时的选择规则，以及返回内容必须整体属于被选中的那份。

// t0 是同时刻场景的基准瞬间：2026-09-10 00:00 UTC。
func latestBaseTime() time.Time {
	return time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
}

// checkLatestA 完整核对 S-A 的返回内容：编号、采样点、采样瞬间、原测量值、
// 逐项所用上限及生效时间、单项与整份结论都必须属于 S-A，
// 不能只选对编号却混入 S-B 的测量值或判定依据。
// S-A：pH 9 > 8@09-01 超标，COD 30 == 30@09-01 达标，整份超标。
func checkLatestA(t *testing.T, smp Sample, label string) {
	t.Helper()
	if smp.ID != "S-A" || smp.PointID != "P1" || smp.Status != StatusConfirmed || smp.VoidReason != "" {
		t.Fatalf("%s: 编号、采样点、状态或作废原因不属于 S-A: %+v", label, smp)
	}
	if !smp.SampledAt.Equal(latestBaseTime()) {
		t.Fatalf("%s: 采样时间应为同一瞬间 %s，实际 %s", label, latestBaseTime(), smp.SampledAt)
	}
	if !smp.Exceeded || len(smp.Measurements) != 2 || len(smp.Results) != 2 {
		t.Fatalf("%s: S-A 应整份超标并带两项测量与判定: %+v", label, smp)
	}
	ms := map[string]float64{}
	for _, m := range smp.Measurements {
		ms[m.Item] = m.Value
	}
	if ms["pH"] != 9 || ms["COD"] != 30 {
		t.Fatalf("%s: 原测量值被换成别的样品: %+v", label, smp.Measurements)
	}
	rs := map[string]ItemResult{}
	for _, r := range smp.Results {
		rs[r.Item] = r
	}
	ph, cod := rs["pH"], rs["COD"]
	if ph.Value != 9 || ph.Limit != 8.0 || !ph.LimitEffective.Equal(at(1, 0)) || !ph.Exceeded {
		t.Fatalf("%s: pH 应为 9 > 8@09-01 超标: %+v", label, ph)
	}
	if cod.Value != 30 || cod.Limit != 30.0 || !cod.LimitEffective.Equal(at(1, 0)) || cod.Exceeded {
		t.Fatalf("%s: COD 应为 30 == 30@09-01 达标: %+v", label, cod)
	}
}

// checkLatestB 完整核对 S-B 的返回内容。
// S-B：pH 7 ≤ 8、COD 20 ≤ 30，两项与整份均达标。
func checkLatestB(t *testing.T, smp Sample, sampled time.Time, label string) {
	t.Helper()
	if smp.ID != "S-B" || smp.PointID != "P1" || smp.Status != StatusConfirmed || smp.VoidReason != "" {
		t.Fatalf("%s: 编号、采样点、状态或作废原因不属于 S-B: %+v", label, smp)
	}
	if !smp.SampledAt.Equal(sampled) {
		t.Fatalf("%s: 采样时间应为 %s，实际 %s", label, sampled, smp.SampledAt)
	}
	if smp.Exceeded || len(smp.Measurements) != 2 || len(smp.Results) != 2 {
		t.Fatalf("%s: S-B 应整份达标并带两项测量与判定: %+v", label, smp)
	}
	ms := map[string]float64{}
	for _, m := range smp.Measurements {
		ms[m.Item] = m.Value
	}
	if ms["pH"] != 7 || ms["COD"] != 20 {
		t.Fatalf("%s: 原测量值被换成别的样品: %+v", label, smp.Measurements)
	}
	rs := map[string]ItemResult{}
	for _, r := range smp.Results {
		rs[r.Item] = r
	}
	ph, cod := rs["pH"], rs["COD"]
	if ph.Value != 7 || ph.Limit != 8.0 || !ph.LimitEffective.Equal(at(1, 0)) || ph.Exceeded {
		t.Fatalf("%s: pH 应为 7 ≤ 8@09-01 达标: %+v", label, ph)
	}
	if cod.Value != 20 || cod.Limit != 30.0 || !cod.LimitEffective.Equal(at(1, 0)) || cod.Exceeded {
		t.Fatalf("%s: COD 应为 20 ≤ 30@09-01 达标: %+v", label, cod)
	}
}

// 同一采样点 S-A、S-B 在同一瞬间采样且均已确认，测量值与判定依据不同。
// 无论录入先后、确认先后（包括 S-A 最后才办理确认），也无论两份各用
// UTC 还是北京时间表示同一瞬间，最近有效结果都必须按编号升序选中 S-A，
// 且返回内容整体来自 S-A。
func TestLatestResultSameInstantConfirmedTieBreak(t *testing.T) {
	t0 := latestBaseTime()
	beijing := time.FixedZone("CST", 8*3600)
	t0Beijing := time.Date(2026, 9, 10, 8, 0, 0, 0, beijing) // 与 t0 同一瞬间
	aMs := []Measurement{{Item: "pH", Value: 9}, {Item: "COD", Value: 30}}
	bMs := []Measurement{{Item: "pH", Value: 7}, {Item: "COD", Value: 20}}

	cases := []struct {
		name string
		// 每份样品用什么时区表示采样瞬间（两者必须是同一瞬间）
		aAt, bAt time.Time
		// 录入与确认的编号顺序
		submitOrder  []string
		confirmOrder []string
	}{
		{
			name: "S-B 最后录入且最后确认",
			aAt:  t0, bAt: t0,
			submitOrder:  []string{"S-A", "S-B"},
			confirmOrder: []string{"S-A", "S-B"},
		},
		{
			name: "S-A 最后录入且最后确认",
			aAt:  t0, bAt: t0,
			submitOrder:  []string{"S-B", "S-A"},
			confirmOrder: []string{"S-B", "S-A"},
		},
		{
			name: "录入与确认顺序交叉",
			aAt:  t0, bAt: t0,
			submitOrder:  []string{"S-A", "S-B"},
			confirmOrder: []string{"S-B", "S-A"},
		},
		{
			name: "S-A 用 UTC、S-B 用北京时间表示同一瞬间",
			aAt:  t0, bAt: t0Beijing,
			submitOrder:  []string{"S-B", "S-A"},
			confirmOrder: []string{"S-B", "S-A"},
		},
		{
			name: "S-A 用北京时间、S-B 用 UTC 表示同一瞬间",
			aAt:  t0Beijing, bAt: t0,
			submitOrder:  []string{"S-A", "S-B"},
			confirmOrder: []string{"S-A", "S-B"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := open(t)
			mustPoint(t, s, "P1", "取水口")
			mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
			mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))

			for _, id := range c.submitOrder {
				if id == "S-A" {
					mustSample(t, s, "S-A", "P1", c.aAt, aMs...)
				} else {
					mustSample(t, s, "S-B", "P1", c.bAt, bMs...)
				}
			}
			for _, id := range c.confirmOrder {
				if _, err := s.Confirm(id); err != nil {
					t.Fatalf("Confirm %s: %v", id, err)
				}
			}

			latest, ok, err := s.LatestResult("P1")
			if err != nil || !ok {
				t.Fatalf("最近有效结果应为 S-A: %+v ok=%v err=%v", latest, ok, err)
			}
			checkLatestA(t, latest, "最近有效结果")
		})
	}
}

// 实际采样时刻只差一纳秒时，必须选择真正较晚的那份（S-B），
// 即使它的编号排在 S-A 后面；编号升序只在时刻完全相同时才生效。
// 较早的 S-A 最后确认也不能把它顶成最近结果。
func TestLatestResultNanosecondLaterWins(t *testing.T) {
	t0 := latestBaseTime()
	tLater := t0.Add(1) // 仅晚一纳秒

	cases := []struct {
		name         string
		confirmOrder []string
	}{
		{"较晚的 S-B 最后确认", []string{"S-A", "S-B"}},
		{"较早的 S-A 最后确认", []string{"S-B", "S-A"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := open(t)
			mustPoint(t, s, "P1", "取水口")
			mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
			mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))
			mustSample(t, s, "S-A", "P1", t0,
				Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
			mustSample(t, s, "S-B", "P1", tLater,
				Measurement{Item: "pH", Value: 7}, Measurement{Item: "COD", Value: 20})
			for _, id := range c.confirmOrder {
				if _, err := s.Confirm(id); err != nil {
					t.Fatalf("Confirm %s: %v", id, err)
				}
			}

			latest, ok, err := s.LatestResult("P1")
			if err != nil || !ok {
				t.Fatalf("最近有效结果应为晚一纳秒的 S-B: %+v ok=%v err=%v", latest, ok, err)
			}
			checkLatestB(t, latest, tLater, "晚一纳秒的最近有效结果")
		})
	}
}

// 采样更晚的待判定样品、确认后又作废（仍保留完整判定依据）的样品，
// 以及其他采样点的更晚结果，都不能抢占当前采样点的最近有效结果。
// 同时刻选中的 S-A 作废后，应退到该时刻下一份仍有效的已确认样品 S-B；
// 全部失效后明确无结果，不能把待判定记录的超标零值当成达标。
func TestLatestResultExcludesPendingVoidedAndOtherPoints(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustPoint(t, s, "P2", "排水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))
	mustLimit(t, s, "P2", "pH", 8.0, at(1, 0))

	t0 := latestBaseTime()
	// S-A、S-B 同一时刻均已确认；S-A 编号在前且整份超标，S-B 达标。
	mustSample(t, s, "S-A", "P1", t0,
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	mustSample(t, s, "S-B", "P1", t0,
		Measurement{Item: "pH", Value: 7}, Measurement{Item: "COD", Value: 20})
	// S-C 采样更晚但始终待判定（测量值超标也没有结论）。
	mustSample(t, s, "S-C", "P1", t0.Add(time.Hour),
		Measurement{Item: "pH", Value: 12}, Measurement{Item: "COD", Value: 40})
	// S-D 采样更晚、已确认后再作废，作废后仍保留完整判定依据。
	mustSample(t, s, "S-D", "P1", t0.Add(2*time.Hour),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	// 另一采样点的更晚已确认结果。
	mustSample(t, s, "S-X", "P2", t0.Add(24*time.Hour), Measurement{Item: "pH", Value: 7})
	for _, id := range []string{"S-B", "S-D", "S-A", "S-X"} {
		if _, err := s.Confirm(id); err != nil {
			t.Fatalf("Confirm %s: %v", id, err)
		}
	}
	if _, err := s.Void("S-D", "复测确认超标"); err != nil {
		t.Fatalf("Void S-D: %v", err)
	}

	// 更晚的待判定、更晚的作废、其他点的更晚结果都不能抢占：仍是 S-A。
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("最近有效结果应为 S-A: %+v ok=%v err=%v", latest, ok, err)
	}
	checkLatestA(t, latest, "存在更晚无效记录时")
	// 其他采样点查它自己的更晚结果，互不串点。
	if other, ok, err := s.LatestResult("P2"); err != nil || !ok || other.ID != "S-X" {
		t.Fatalf("P2 的最近有效结果应为 S-X: %+v ok=%v err=%v", other, ok, err)
	}

	// 作废记录的完整判定依据仍保留在按点列表里，但不能因此再次成为有效结果。
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 4 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	find := func(id string) Sample {
		t.Helper()
		for _, smp := range list {
			if smp.ID == id {
				return smp
			}
		}
		t.Fatalf("列表中缺少 %s: %+v", id, list)
		return Sample{}
	}
	d := find("S-D")
	if d.Status != StatusVoided || d.VoidReason != "复测确认超标" || !d.Exceeded || len(d.Results) != 2 {
		t.Fatalf("S-D 作废后仍应保留完整判定依据: %+v", d)
	}
	if d.Results[0].Limit != 8.0 || !d.Results[0].LimitEffective.Equal(at(1, 0)) {
		t.Fatalf("S-D 的逐项上限与生效时间应保留: %+v", d.Results)
	}

	// 同时刻被选中的 S-A 作废：退到该时刻下一份仍有效的已确认样品 S-B，
	// 而不是更晚的待判定 S-C 或仍保留结果的作废 S-D。
	if _, err := s.Void("S-A", "采样瓶破损"); err != nil {
		t.Fatalf("Void S-A: %v", err)
	}
	fallback, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("S-A 作废后应退到 S-B: %+v ok=%v err=%v", fallback, ok, err)
	}
	checkLatestB(t, fallback, t0, "S-A 作废后")

	// 关闭重开后选择不变，作废状态与退回规则随落盘保持。
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	reopened, ok, err := s2.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("重开后仍应为 S-B: %+v ok=%v err=%v", reopened, ok, err)
	}
	checkLatestB(t, reopened, t0, "重开后")

	// 最后一份有效样品也作废：明确无结果，返回空样品且不报错误。
	// 更晚的 S-C 仍待判定，其 Exceeded 零值为假不表示达标，不能顶替。
	if _, err := s2.Void("S-B", "记录错误"); err != nil {
		t.Fatalf("Void S-B: %v", err)
	}
	none, ok, err := s2.LatestResult("P1")
	if err != nil || ok {
		t.Fatalf("全部失效时应明确无结果: %+v ok=%v err=%v", none, ok, err)
	}
	if none.ID != "" || none.Status != "" || none.Results != nil {
		t.Fatalf("无结果时应返回空样品，不能带上待判定记录的信息: %+v", none)
	}
	pending, err := s2.ListByPoint("P1")
	if err != nil {
		t.Fatalf("ListByPoint: %v", err)
	}
	var c Sample
	for _, smp := range pending {
		if smp.ID == "S-C" {
			c = smp
		}
	}
	if c.ID != "S-C" {
		t.Fatalf("列表中缺少 S-C: %+v", pending)
	}
	if c.Status != StatusPending || len(c.Results) != 0 || c.Exceeded {
		t.Fatalf("S-C 应保持待判定、无结论，超标零值不能解释成达标: %+v", c)
	}
}

// 该点只有待判定样品、或仅有已作废样品时，查询明确表示无结果：
// 返回空样品、ok 为假且不报错。
func TestLatestResultWithoutValidConfirmedSample(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))

	// 仅有待判定样品：Exceeded 的零值 false 是“没有结论”，不是达标。
	mustSample(t, s, "S1", "P1", latestBaseTime(), Measurement{Item: "pH", Value: 7})
	none, ok, err := s.LatestResult("P1")
	if err != nil || ok {
		t.Fatalf("仅待判定时应无结果: %+v ok=%v err=%v", none, ok, err)
	}
	if none.ID != "" || none.Status != "" || none.Exceeded || none.Results != nil {
		t.Fatalf("无结果应返回空样品，不能把待判定零值当达标: %+v", none)
	}

	// 确认后又作废：即使判定依据仍保留，也不再是有效结果。
	if _, err := s.Confirm("S1"); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if _, err := s.Void("S1", "采样瓶破损"); err != nil {
		t.Fatalf("Void: %v", err)
	}
	none2, ok, err := s.LatestResult("P1")
	if err != nil || ok {
		t.Fatalf("仅作废样品时应无结果: %+v ok=%v err=%v", none2, ok, err)
	}
	if none2.ID != "" || none2.Status != "" || none2.Results != nil {
		t.Fatalf("无结果应返回空样品，不能带回作废记录的判定: %+v", none2)
	}
}
