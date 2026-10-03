package water

import (
	"errors"
	"testing"
)

// 调用方拿到样品记录后为页面展示修改本地数据：只能改动这次拿到的记录，
// 不能写回数据存放，也不能影响其他已经返回的记录。
// 已确认样品含两个项目：pH 9 大于上限 8 超标，COD 30 恰好等于上限 30 达标。
func TestReturnedRecordMutationDoesNotLeak(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))
	mustSample(t, s, "S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	if _, err := s.Confirm("S1"); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	checkSaved := func(smp Sample, label string) {
		t.Helper()
		if smp.ID != "S1" || smp.PointID != "P1" || !smp.SampledAt.Equal(at(10, 0)) {
			t.Fatalf("%s: 样品归属或采样时间变了: %+v", label, smp)
		}
		if smp.Status != StatusConfirmed || !smp.Exceeded || smp.VoidReason != "" {
			t.Fatalf("%s: 状态或超标标记变了: %+v", label, smp)
		}
		if len(smp.Measurements) != 2 || len(smp.Results) != 2 {
			t.Fatalf("%s: 测量或判定不完整: %+v", label, smp)
		}
		ms := map[string]float64{}
		for _, m := range smp.Measurements {
			ms[m.Item] = m.Value
		}
		if ms["pH"] != 9 || ms["COD"] != 30 {
			t.Fatalf("%s: 原测量变了: %+v", label, smp.Measurements)
		}
		rs := map[string]ItemResult{}
		for _, r := range smp.Results {
			rs[r.Item] = r
		}
		ph, cod := rs["pH"], rs["COD"]
		if ph.Value != 9 || ph.Limit != 8.0 || !ph.LimitEffective.Equal(at(1, 0)) || !ph.Exceeded {
			t.Fatalf("%s: pH 判定变了: %+v", label, ph)
		}
		if cod.Value != 30 || cod.Limit != 30.0 || !cod.LimitEffective.Equal(at(1, 0)) || cod.Exceeded {
			t.Fatalf("%s: COD 判定变了: %+v", label, cod)
		}
	}

	// 基线：按点查询能看到原测量、每项所用上限和生效时间、单项与整份超标标记；
	// 最近有效结果指向这份样品
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	checkSaved(list[0], "baseline list")
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("LatestResult: ok=%v err=%v", ok, err)
	}
	checkSaved(latest, "baseline latest")

	// 调用方就地修改拿到的记录：项目名称、测量值、上限、生效时间、
	// 单项与整份超标标记，以及样品级字段，全部改成另一套“结论”
	m := list[0]
	m.ID = "S9"
	m.PointID = "P9"
	m.SampledAt = at(20, 0)
	m.Status = StatusVoided
	m.Exceeded = false
	m.VoidReason = "本地编辑"
	m.Measurements[0].Item = "SS"
	m.Measurements[0].Value = 1
	m.Measurements[1].Value = 999
	m.Results[0].Item = "SS"
	m.Results[0].Value = 1
	m.Results[0].Limit = 100
	m.Results[0].LimitEffective = at(25, 0)
	m.Results[0].Exceeded = false
	m.Results[1].Exceeded = true
	m.Results = append(m.Results, ItemResult{Item: "TN", Value: 1, Limit: 2})

	// 修改列表里这份记录，不影响此前已经取得的最近有效结果
	checkSaved(latest, "latest after mutating list copy")

	// 再次查询、重复确认、最近有效结果仍必须返回原来已经保存的内容
	list2, err := s.ListByPoint("P1")
	if err != nil || len(list2) != 1 {
		t.Fatalf("re-query ListByPoint: %+v err=%v", list2, err)
	}
	checkSaved(list2[0], "re-query list")
	again, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("re-confirm: %v", err)
	}
	checkSaved(again, "re-confirm")
	latest2, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("re-query LatestResult: ok=%v err=%v", ok, err)
	}
	checkSaved(latest2, "re-query latest")
}

// 列表里的样品记录与最近有效结果分别取得时互不影响：修改其中一份，
// 另一份已取得的记录保持原内容；改掉本地记录的编号、采样点、采样时间
// 或状态，不能改变保存记录的归属、排列位置和是否属于最近有效结果。
func TestListAndLatestRecordsIndependent(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustPoint(t, s, "P2", "排水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))

	mustSample(t, s, "S1", "P1", at(5, 0), Measurement{Item: "pH", Value: 7})
	mustSample(t, s, "S2", "P1", at(9, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	mustSample(t, s, "S3", "P1", at(7, 0), Measurement{Item: "pH", Value: 6})
	if _, err := s.Confirm("S2"); err != nil {
		t.Fatalf("Confirm S2: %v", err)
	}

	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 3 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S2" {
		t.Fatalf("LatestResult: %+v ok=%v err=%v", latest, ok, err)
	}

	// 改掉列表记录的编号、采样点、采样时间、状态
	for i := range list {
		list[i].ID = "X"
		list[i].PointID = "P2"
		list[i].SampledAt = at(1, 0)
		list[i].Status = StatusVoided
	}
	// 分别取得的最近有效结果保持原内容
	if latest.ID != "S2" || latest.PointID != "P1" || !latest.SampledAt.Equal(at(9, 0)) ||
		latest.Status != StatusConfirmed || !latest.Exceeded {
		t.Fatalf("mutating list copies changed the latest record: %+v", latest)
	}

	// 保存记录的归属、排列位置和最近有效结果成员资格不变
	list2, err := s.ListByPoint("P1")
	if err != nil || len(list2) != 3 {
		t.Fatalf("re-query ListByPoint: %+v err=%v", list2, err)
	}
	want := []string{"S2", "S3", "S1"}
	for i, id := range want {
		if list2[i].ID != id || list2[i].PointID != "P1" {
			t.Fatalf("stored order/ownership changed at %d: %+v", i, list2)
		}
	}
	if !list2[0].SampledAt.Equal(at(9, 0)) || list2[0].Status != StatusConfirmed {
		t.Fatalf("stored S2 changed: %+v", list2[0])
	}
	if listP2, err := s.ListByPoint("P2"); err != nil || len(listP2) != 0 {
		t.Fatalf("P2 should stay empty, got %+v err=%v", listP2, err)
	}
	latest2, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest2.ID != "S2" {
		t.Fatalf("latest valid membership changed: %+v ok=%v err=%v", latest2, ok, err)
	}

	// 项目不止一项时，各项的原始测量和判定依据都保持完整：
	// 替换最近有效结果里的一个项目、截短测量，已取得的列表记录与
	// 再次查询都仍是完整的两项
	latest2.Results[0] = ItemResult{Item: "SS", Value: 1, Limit: 2, Exceeded: false}
	latest2.Measurements = latest2.Measurements[:1]
	for _, smp := range list2 {
		if smp.ID != "S2" {
			continue
		}
		if len(smp.Measurements) != 2 || len(smp.Results) != 2 {
			t.Fatalf("previously fetched list record lost items: %+v", smp)
		}
	}
	latest3, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest3.ID != "S2" {
		t.Fatalf("re-query LatestResult: %+v ok=%v err=%v", latest3, ok, err)
	}
	if len(latest3.Measurements) != 2 || len(latest3.Results) != 2 {
		t.Fatalf("stored sample lost items: %+v", latest3)
	}
	rs := map[string]ItemResult{}
	for _, r := range latest3.Results {
		rs[r.Item] = r
	}
	ph, cod := rs["pH"], rs["COD"]
	if ph.Value != 9 || ph.Limit != 8.0 || !ph.LimitEffective.Equal(at(1, 0)) || !ph.Exceeded {
		t.Fatalf("pH item replaced: %+v", ph)
	}
	if cod.Value != 30 || cod.Limit != 30.0 || !cod.LimitEffective.Equal(at(1, 0)) || cod.Exceeded {
		t.Fatalf("COD item replaced: %+v", cod)
	}
}

// 已确认样品经正常作废后，按点查询保留原测量、原判定和作废原因；
// 调用方把查询得到的作废记录改回已确认或清空作废原因，不能使保存记录
// 恢复有效；作废前已取得的已确认记录保持取得时的内容。
func TestVoidedRecordMutationDoesNotRestore(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustPoint(t, s, "P2", "排水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))
	mustLimit(t, s, "P2", "pH", 8.0, at(1, 0))

	// S0 较早确认，S1 更晚确认；P2 的 SX 是唯一一份样品
	mustSample(t, s, "S0", "P1", at(5, 0), Measurement{Item: "pH", Value: 7})
	mustSample(t, s, "S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	mustSample(t, s, "SX", "P2", at(10, 0), Measurement{Item: "pH", Value: 9})
	if _, err := s.Confirm("S0"); err != nil {
		t.Fatalf("Confirm S0: %v", err)
	}
	conf, err := s.Confirm("S1") // 作废前取得的已确认记录
	if err != nil {
		t.Fatalf("Confirm S1: %v", err)
	}
	if _, err := s.Confirm("SX"); err != nil {
		t.Fatalf("Confirm SX: %v", err)
	}

	if _, err := s.Void("S1", "采样瓶破损"); err != nil {
		t.Fatalf("Void S1: %v", err)
	}
	if _, err := s.Void("SX", "记录错误"); err != nil {
		t.Fatalf("Void SX: %v", err)
	}

	checkVoided := func(smp Sample, label string) {
		t.Helper()
		if smp.ID != "S1" || smp.Status != StatusVoided || smp.VoidReason != "采样瓶破损" {
			t.Fatalf("%s: 作废状态或原因变了: %+v", label, smp)
		}
		if !smp.Exceeded || len(smp.Measurements) != 2 || len(smp.Results) != 2 {
			t.Fatalf("%s: 原测量或原判定丢失: %+v", label, smp)
		}
		rs := map[string]ItemResult{}
		for _, r := range smp.Results {
			rs[r.Item] = r
		}
		if ph := rs["pH"]; ph.Value != 9 || ph.Limit != 8.0 || !ph.Exceeded {
			t.Fatalf("%s: pH 原判定变了: %+v", label, ph)
		}
		if cod := rs["COD"]; cod.Value != 30 || cod.Limit != 30.0 || cod.Exceeded {
			t.Fatalf("%s: COD 原判定变了: %+v", label, cod)
		}
	}

	// 作废前取得的已确认记录保持取得时的内容，不随作废操作变化
	if conf.Status != StatusConfirmed || conf.VoidReason != "" || !conf.Exceeded || len(conf.Results) != 2 {
		t.Fatalf("pre-void confirmed record changed: %+v", conf)
	}

	// 按点查询保留原测量、原判定结果和作废原因
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	var rec Sample
	for _, smp := range list {
		if smp.ID == "S1" {
			rec = smp
		}
	}
	checkVoided(rec, "voided list record")

	// 调用方把作废记录改回已确认、清空作废原因
	rec.Status = StatusConfirmed
	rec.VoidReason = ""

	// 保存记录不恢复有效：再次确认仍拒绝
	if _, err := s.Confirm("S1"); !errors.Is(err, ErrVoided) {
		t.Fatalf("confirm voided should stay rejected, got %v", err)
	}
	// 最近有效结果继续排除该样品，落到较早的 S0
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S0" {
		t.Fatalf("latest should exclude voided S1 and be S0, got %+v ok=%v err=%v", latest, ok, err)
	}
	// 没有其他有效结果的采样点明确返回无结果
	if _, ok, err := s.LatestResult("P2"); err != nil || ok {
		t.Fatalf("P2 should have no valid result, ok=%v err=%v", ok, err)
	}

	// 再次查询仍是作废记录：原测量、原判定、作废原因俱在
	list2, err := s.ListByPoint("P1")
	if err != nil || len(list2) != 2 {
		t.Fatalf("re-query ListByPoint: %+v err=%v", list2, err)
	}
	for _, smp := range list2 {
		if smp.ID == "S1" {
			checkVoided(smp, "re-query voided record")
		}
	}

	// 作废前取得的已确认记录仍不随后续操作变化
	if conf.Status != StatusConfirmed || conf.VoidReason != "" || !conf.Exceeded || len(conf.Results) != 2 {
		t.Fatalf("pre-void confirmed record changed after later operations: %+v", conf)
	}
}
