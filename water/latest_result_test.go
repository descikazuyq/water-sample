package water

import (
	"testing"
	"time"
)

// 本文件的测试只针对 LatestResult 的选择规则：同一采样点上多份已确认且
// 未作废的样品中，采样时刻最晚的一份是最近有效结果；采样时刻完全相同的
// 多份之间按样品编号升序选择。录入先后、确认先后、是否超标都不改变这个
// 次序。公开查询入口、ListByPoint 的排序和逐项判定规则保持现状。

// checkSampleA 核对 S-A 应保存的完整内容：pH 9 > 8@09-01 超标，整份超标。
// 与 S-B 同时刻同采样点，用来验证选中的不只是编号，判定依据也没有混入另一份。
func checkSampleA(t *testing.T, smp Sample, label string) {
	t.Helper()
	if smp.ID != "S-A" || smp.PointID != "P1" || smp.Status != StatusConfirmed || smp.VoidReason != "" {
		t.Fatalf("%s: 基本字段、状态或作废原因不属于 S-A: %+v", label, smp)
	}
	if !smp.SampledAt.Equal(at(10, 0)) || smp.SampledAt.Location() != time.UTC {
		t.Fatalf("%s: 采样时间应保存为 09-10 00:00 UTC: %+v", label, smp.SampledAt)
	}
	if !smp.Exceeded || len(smp.Measurements) != 1 || len(smp.Results) != 1 {
		t.Fatalf("%s: 整份结论或记录数量不属于 S-A: %+v", label, smp)
	}
	if m := smp.Measurements[0]; m.Item != "pH" || m.Value != 9 {
		t.Fatalf("%s: 原测量值不属于 S-A: %+v", label, m)
	}
	if r := smp.Results[0]; r.Item != "pH" || r.Value != 9 || r.Limit != 8.0 ||
		!r.LimitEffective.Equal(at(1, 0)) || !r.Exceeded {
		t.Fatalf("%s: 逐项判定依据不属于 S-A（9 > 8@09-01 超标）: %+v", label, r)
	}
}

// checkSampleB 核对 S-B 应保存的完整内容：pH 7 ≤ 8、COD 30 == 30@09-01，整份达标。
// sampledAt 是该样品应有的采样时刻（纳秒测试中比整点晚一纳秒）。
func checkSampleB(t *testing.T, smp Sample, sampledAt time.Time, label string) {
	t.Helper()
	if smp.ID != "S-B" || smp.PointID != "P1" || smp.Status != StatusConfirmed || smp.VoidReason != "" {
		t.Fatalf("%s: 基本字段、状态或作废原因不属于 S-B: %+v", label, smp)
	}
	if !smp.SampledAt.Equal(sampledAt) {
		t.Fatalf("%s: 采样时间被改动: %+v", label, smp)
	}
	if smp.Exceeded || len(smp.Measurements) != 2 || len(smp.Results) != 2 {
		t.Fatalf("%s: 整份结论或记录数量不属于 S-B: %+v", label, smp)
	}
	ms := map[string]float64{}
	for _, m := range smp.Measurements {
		ms[m.Item] = m.Value
	}
	if ms["pH"] != 7 || ms["COD"] != 30 {
		t.Fatalf("%s: 原测量值不属于 S-B: %+v", label, smp.Measurements)
	}
	rs := map[string]ItemResult{}
	for _, r := range smp.Results {
		rs[r.Item] = r
	}
	if ph := rs["pH"]; ph.Value != 7 || ph.Limit != 8.0 || !ph.LimitEffective.Equal(at(1, 0)) || ph.Exceeded {
		t.Fatalf("%s: pH 逐项判定依据不属于 S-B: %+v", label, ph)
	}
	if cod := rs["COD"]; cod.Value != 30 || cod.Limit != 30.0 || !cod.LimitEffective.Equal(at(1, 0)) || cod.Exceeded {
		t.Fatalf("%s: COD 逐项判定依据不属于 S-B: %+v", label, cod)
	}
}

// 确认先后不改变选择：较早采样的样品在较晚采样的样品之后确认，
// 最近有效结果仍指向采样较晚的那份，不能把最后办理确认的记录
// 误当成最近采样结果；录入顺序同样无关。
func TestLatestResultIgnoresConfirmOrder(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))

	// 较晚采样的样品先录入、先确认；较早采样的样品后录入、最后确认
	mustSample(t, s, "S-NEW", "P1", at(8, 0), Measurement{Item: "pH", Value: 9})
	if _, err := s.Confirm("S-NEW"); err != nil {
		t.Fatalf("Confirm S-NEW: %v", err)
	}
	mustSample(t, s, "S-OLD", "P1", at(5, 0), Measurement{Item: "pH", Value: 7})
	if _, err := s.Confirm("S-OLD"); err != nil {
		t.Fatalf("Confirm S-OLD: %v", err)
	}

	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("LatestResult: %+v ok=%v err=%v", latest, ok, err)
	}
	if latest.ID != "S-NEW" || !latest.SampledAt.Equal(at(8, 0)) {
		t.Fatalf("最后确认的较早样品不能顶替采样较晚的结果: %+v", latest)
	}
	if !latest.Exceeded || latest.Results[0].Value != 9 || latest.Results[0].Limit != 8.0 {
		t.Fatalf("最近有效结果的判定依据应属于 S-NEW: %+v", latest.Results[0])
	}
}

// 同一采样点的 S-A 与 S-B 在同一时刻采样且都已确认，测量值与判定依据不同：
// S-B 先录入、最后确认，选择仍按编号升序落在 S-A；返回内容完整对应 S-A，
// 不能只选对编号却混入 S-B 的判定依据。S-A 超标、S-B 达标，是否超标不改变次序。
func TestLatestResultSameInstantByID(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))

	// S-B 先录入；S-A 后录入但先确认，S-B 最后办理确认
	mustSample(t, s, "S-B", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 7}, Measurement{Item: "COD", Value: 30})
	mustSample(t, s, "S-A", "P1", at(10, 0), Measurement{Item: "pH", Value: 9})
	if _, err := s.Confirm("S-A"); err != nil {
		t.Fatalf("Confirm S-A: %v", err)
	}
	if _, err := s.Confirm("S-B"); err != nil {
		t.Fatalf("Confirm S-B: %v", err)
	}

	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("LatestResult: %+v ok=%v err=%v", latest, ok, err)
	}
	checkSampleA(t, latest, "同时刻按编号升序选择")
}

// 同一瞬间用不同时区表示也算同一时刻：S-A 用 UTC、S-B 用北京时间录入，
// 两份都已确认，选择仍按编号升序落在 S-A，与全部用 UTC 录入时相同。
func TestLatestResultSameInstantAcrossTimezones(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))

	beijing := time.FixedZone("CST", 8*3600)
	sameInstant := time.Date(2026, 9, 10, 8, 0, 0, 0, beijing) // == at(10,0) UTC
	mustSample(t, s, "S-B", "P1", sameInstant,
		Measurement{Item: "pH", Value: 7}, Measurement{Item: "COD", Value: 30})
	mustSample(t, s, "S-A", "P1", at(10, 0), Measurement{Item: "pH", Value: 9})
	if _, err := s.Confirm("S-B"); err != nil {
		t.Fatalf("Confirm S-B: %v", err)
	}
	if _, err := s.Confirm("S-A"); err != nil {
		t.Fatalf("Confirm S-A: %v", err)
	}

	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("LatestResult: %+v ok=%v err=%v", latest, ok, err)
	}
	checkSampleA(t, latest, "不同时区表示同一瞬间")
}

// 采样时刻只差一纳秒就不算同时刻：选择真正较晚的那份，
// 即使它的编号排在后面，编号升序只在时刻完全相同时适用。
func TestLatestResultNanosecondLaterWins(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))

	// S-A 采样于整点；S-B 编号排在后面，但采样时刻晚一纳秒
	mustSample(t, s, "S-A", "P1", at(10, 0), Measurement{Item: "pH", Value: 9})
	mustSample(t, s, "S-B", "P1", at(10, 0).Add(time.Nanosecond),
		Measurement{Item: "pH", Value: 7}, Measurement{Item: "COD", Value: 30})
	if _, err := s.Confirm("S-A"); err != nil {
		t.Fatalf("Confirm S-A: %v", err)
	}
	if _, err := s.Confirm("S-B"); err != nil {
		t.Fatalf("Confirm S-B: %v", err)
	}

	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("LatestResult: %+v ok=%v err=%v", latest, ok, err)
	}
	if latest.ID != "S-B" {
		t.Fatalf("晚一纳秒的 S-B 应被选中，即使编号排在后面: %+v", latest)
	}
	if !latest.SampledAt.Equal(at(10, 0).Add(time.Nanosecond)) {
		t.Fatalf("选中样品的采样时刻应晚一纳秒: %+v", latest.SampledAt)
	}
	checkSampleB(t, latest, at(10, 0).Add(time.Nanosecond), "一纳秒之差选真正较晚者")
}

// 有效记录之外的记录不能抢占最近有效结果：采样更晚的待判定样品、
// 已确认后又作废的样品（即使保留完整判定依据）、其他采样点的更晚结果
// 都被排除。选中的同时刻样品作废后，改为返回该时刻下一份仍有效的
// 已确认样品。
func TestLatestResultSkipsInvalidAndFallsBack(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustPoint(t, s, "P2", "排水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))
	mustLimit(t, s, "P2", "pH", 8.0, at(1, 0))

	// 同时刻的 S-A、S-B 均已确认，S-A 按编号升序当选
	mustSample(t, s, "S-A", "P1", at(10, 0), Measurement{Item: "pH", Value: 9})
	mustSample(t, s, "S-B", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 7}, Measurement{Item: "COD", Value: 30})
	if _, err := s.Confirm("S-A"); err != nil {
		t.Fatalf("Confirm S-A: %v", err)
	}
	if _, err := s.Confirm("S-B"); err != nil {
		t.Fatalf("Confirm S-B: %v", err)
	}
	// 采样更晚的待判定样品：不能抢占
	mustSample(t, s, "S-PEND", "P1", at(20, 0), Measurement{Item: "pH", Value: 9})
	// 采样更晚、已确认又作废的样品：保留完整判定依据也不能再算有效
	mustSample(t, s, "S-VOID", "P1", at(15, 0), Measurement{Item: "pH", Value: 9})
	if _, err := s.Confirm("S-VOID"); err != nil {
		t.Fatalf("Confirm S-VOID: %v", err)
	}
	if _, err := s.Void("S-VOID", "采样瓶破损"); err != nil {
		t.Fatalf("Void S-VOID: %v", err)
	}
	// 其他采样点更晚的已确认结果：不能抢占本采样点
	mustSample(t, s, "S-ELSE", "P2", at(25, 0), Measurement{Item: "pH", Value: 9})
	if _, err := s.Confirm("S-ELSE"); err != nil {
		t.Fatalf("Confirm S-ELSE: %v", err)
	}

	// 作废记录确实保留着完整判定依据，但仍被排除
	list, err := s.ListByPoint("P1")
	if err != nil {
		t.Fatalf("ListByPoint: %v", err)
	}
	var voided Sample
	for _, smp := range list {
		if smp.ID == "S-VOID" {
			voided = smp
		}
	}
	if voided.Status != StatusVoided || len(voided.Results) != 1 || !voided.Exceeded ||
		voided.Results[0].Limit != 8.0 {
		t.Fatalf("作废记录应保留完整判定依据: %+v", voided)
	}

	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("LatestResult: %+v ok=%v err=%v", latest, ok, err)
	}
	checkSampleA(t, latest, "排除待判定、已作废与其他采样点")

	// 选中的同时刻样品作废后：退到该时刻下一份仍有效的已确认样品 S-B，
	// 而不是采样更早或更晚的记录，也不是无结果
	if _, err := s.Void("S-A", "复测后作废"); err != nil {
		t.Fatalf("Void S-A: %v", err)
	}
	fallback, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("S-A 作废后 LatestResult: %+v ok=%v err=%v", fallback, ok, err)
	}
	checkSampleB(t, fallback, at(10, 0), "同时刻下一份仍有效样品")
}

// 该点没有任何有效的已确认样品时，查询明确表示无结果：返回空样品且不报错误，
// 不能把待判定记录的超标标记 false 解释成达标结论。
func TestLatestResultNoValidSample(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))

	// 只有一份待判定样品，其测量值若判定会超标；没有已确认记录
	mustSample(t, s, "S-PEND", "P1", at(10, 0), Measurement{Item: "pH", Value: 9})

	latest, ok, err := s.LatestResult("P1")
	if err != nil {
		t.Fatalf("无有效结果不应报错: %v", err)
	}
	if ok {
		t.Fatalf("只有待判定样品时不能给出有效结果: %+v", latest)
	}
	if latest.ID != "" || latest.Status != "" || latest.Results != nil || latest.Exceeded {
		t.Fatalf("无结果时应返回空样品，不能把待判定记录当达标结论: %+v", latest)
	}

	// 唯一的已确认样品作废后同样明确无结果
	if _, err := s.Confirm("S-PEND"); err != nil {
		t.Fatalf("Confirm S-PEND: %v", err)
	}
	if _, err := s.Void("S-PEND", "记录错误"); err != nil {
		t.Fatalf("Void S-PEND: %v", err)
	}
	latest2, ok, err := s.LatestResult("P1")
	if err != nil || ok {
		t.Fatalf("全部作废后应无结果: %+v ok=%v err=%v", latest2, ok, err)
	}
	if latest2.ID != "" || latest2.Status != "" {
		t.Fatalf("无结果时应返回空样品: %+v", latest2)
	}
}
