package water

import (
	"errors"
	"testing"
	"time"
)

// 本文件的测试只针对一件事：调用方拿到的样品记录是这次返回的副本，
// 为页面展示随意修改本地数据，既不能写回数据存放，也不能影响其他
// 已经返回的记录。ListByPoint、LatestResult、Confirm 的入口、返回含义
// 与排序规则保持现状。

// twoItemSaved 是"一项超标、一项恰好等于上限"的已确认样品应保存的完整内容。
func checkTwoItemSaved(t *testing.T, smp Sample, label string) {
	t.Helper()
	if smp.ID != "S1" || smp.PointID != "P1" || smp.Status != StatusConfirmed {
		t.Fatalf("%s: 样品基本字段被改动: %+v", label, smp)
	}
	if !smp.SampledAt.Equal(at(10, 0)) || smp.VoidReason != "" {
		t.Fatalf("%s: 采样时间或作废原因被改动: %+v", label, smp)
	}
	if !smp.Exceeded {
		t.Fatalf("%s: 整份样品应超标（pH 9 > 8）: %+v", label, smp)
	}
	if len(smp.Measurements) != 2 || len(smp.Results) != 2 {
		t.Fatalf("%s: 测量项目或判定结果数量被改动: %+v", label, smp)
	}
	ms := map[string]float64{}
	for _, m := range smp.Measurements {
		ms[m.Item] = m.Value
	}
	if ms["pH"] != 9 || ms["COD"] != 30 {
		t.Fatalf("%s: 原测量值被改动: %+v", label, smp.Measurements)
	}
	rs := map[string]ItemResult{}
	for _, r := range smp.Results {
		rs[r.Item] = r
	}
	ph, ok := rs["pH"]
	if !ok || ph.Value != 9 || ph.Limit != 8.0 || !ph.LimitEffective.Equal(at(1, 0)) || !ph.Exceeded {
		t.Fatalf("%s: pH 应为 9 > 8@09-01 超标: %+v", label, ph)
	}
	cod, ok := rs["COD"]
	if !ok || cod.Value != 30 || cod.Limit != 30.0 || !cod.LimitEffective.Equal(at(1, 0)) || cod.Exceeded {
		t.Fatalf("%s: COD 30 == 上限 30@09-01 应达标: %+v", label, cod)
	}
}

// 同一采样点一份已确认样品（pH 9 > 上限 8 超标，COD 30 == 上限 30 达标）。
// 按点查询能看到原测量、每项所用上限与生效时间、单项与整份超标标记，
// 最近有效结果指向这份样品。调用方把返回记录里的项目名称、测量值、上限、
// 生效时间、超标标记乃至编号、状态全部改掉后，再次查询和重复确认仍必须
// 返回原来保存的内容。
func TestReturnedRecordMutationDoesNotWriteBack(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))
	mustSample(t, s, "S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	conf, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	checkTwoItemSaved(t, conf, "confirm 返回值")

	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	checkTwoItemSaved(t, list[0], "按点查询")
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("最近有效结果应指向 S1: %+v ok=%v err=%v", latest, ok, err)
	}
	checkTwoItemSaved(t, latest, "最近有效结果")

	// 调用方为页面展示肆意修改拿到的记录：基本字段、测量、逐项判定全改
	m := list[0]
	m.ID = "HACK"
	m.PointID = "P9"
	m.SampledAt = at(1, 0)
	m.Status = StatusVoided
	m.Exceeded = false
	m.VoidReason = "伪造原因"
	m.Measurements[0].Item = "SS"
	m.Measurements[0].Value = 0.1
	m.Measurements[1].Value = 999
	m.Results[0].Item = "SS"
	m.Results[0].Value = 0.1
	m.Results[0].Limit = 1000
	m.Results[0].LimitEffective = at(20, 0)
	m.Results[0].Exceeded = false
	m.Results[1].Limit = 0.01
	m.Results[1].Exceeded = true

	// 再次查询：保存记录必须仍是原内容
	list2, err := s.ListByPoint("P1")
	if err != nil || len(list2) != 1 {
		t.Fatalf("ListByPoint after mutation: %+v err=%v", list2, err)
	}
	checkTwoItemSaved(t, list2[0], "修改后再次查询")
	// 重复确认：仍返回已保存结论，不能采纳调用方临时编辑的结果
	again, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("重复确认: %v", err)
	}
	checkTwoItemSaved(t, again, "修改后重复确认")
	// 最近有效结果也不受影响
	latest2, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("修改后最近有效结果: %+v ok=%v err=%v", latest2, ok, err)
	}
	checkTwoItemSaved(t, latest2, "修改后最近有效结果")
	// 修改前取得的 latest 副本保持取得时的内容
	checkTwoItemSaved(t, latest, "先前取得的最近有效结果")
}

// 列表记录与最近有效结果分别取得时互不影响：改其中一份，另一份保持原样；
// 改掉本地记录的编号、采样点、采样时间或状态，不能改变保存记录的归属、
// 排列位置和是否属于最近有效结果；两个项目的原始测量与判定依据都保持完整，
// 不允许只保住整份超标标记而某个项目被替换。
func TestListAndLatestRecordsAreIndependent(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustPoint(t, s, "P2", "排水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))
	mustLimit(t, s, "P2", "pH", 8.0, at(1, 0))

	// S0 较早、S1 较晚且为双项目样品；P2 的 SX 用来检查归属不被改写
	mustSample(t, s, "S0", "P1", at(5, 0), Measurement{Item: "pH", Value: 7})
	mustSample(t, s, "S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	mustSample(t, s, "SX", "P2", at(20, 0), Measurement{Item: "pH", Value: 7})
	if _, err := s.Confirm("S0"); err != nil {
		t.Fatalf("Confirm S0: %v", err)
	}
	if _, err := s.Confirm("S1"); err != nil {
		t.Fatalf("Confirm S1: %v", err)
	}

	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	if list[0].ID != "S1" || list[1].ID != "S0" {
		t.Fatalf("排列应为 S1 在前 S0 在后: %+v", list)
	}
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("最近有效结果应为 S1: %+v ok=%v err=%v", latest, ok, err)
	}

	// 改最近有效结果这份记录：换掉其中一个项目的判定、伪造归属与状态
	latest.ID = "S0"
	latest.PointID = "P2"
	latest.SampledAt = at(1, 0)
	latest.Status = StatusVoided
	latest.Exceeded = false
	latest.Results[1] = ItemResult{Item: "SS", Value: 1, Limit: 2, LimitEffective: at(2, 0), Exceeded: false}
	latest.Measurements = append(latest.Measurements, Measurement{Item: "SS", Value: 1})

	// 列表里先取得的那份记录不受 latest 副本修改的影响
	checkTwoItemSaved(t, list[0], "修改 latest 后列表中的记录")

	// 再改列表记录：编号、采样点、采样时间、状态全部伪造
	list[0].ID = "ZZZ"
	list[0].PointID = "P2"
	list[0].SampledAt = time.Time{}
	list[0].Status = StatusPending
	list[0].Results[0].Exceeded = false

	// 保存记录的归属、排列位置、最近有效结果身份不变
	list2, err := s.ListByPoint("P1")
	if err != nil || len(list2) != 2 {
		t.Fatalf("修改后 ListByPoint: %+v err=%v", list2, err)
	}
	if list2[0].ID != "S1" || list2[1].ID != "S0" {
		t.Fatalf("修改本地记录不应改变排列位置: %+v", list2)
	}
	checkTwoItemSaved(t, list2[0], "修改列表记录后再次查询")
	// 两个项目各自的原始测量与判定依据都完整，COD 一项不能被替换掉
	if list2[0].Results[0].Item == "SS" || list2[0].Results[1].Item == "SS" {
		t.Fatalf("某个项目被替换: %+v", list2[0].Results)
	}
	latest2, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest2.ID != "S1" {
		t.Fatalf("修改本地记录不应改变最近有效结果: %+v ok=%v err=%v", latest2, ok, err)
	}
	checkTwoItemSaved(t, latest2, "修改后最近有效结果")
	// P2 的归属不被伪造的 PointID 改变
	listP2, err := s.ListByPoint("P2")
	if err != nil || len(listP2) != 1 || listP2[0].ID != "SX" {
		t.Fatalf("P2 的样品归属被改动: %+v err=%v", listP2, err)
	}
}

// 已确认样品经正常作废后，按点查询保留原测量、原判定结果和作废原因。
// 调用方把查询得到的作废记录改回已确认、清空作废原因，不能使保存记录
// 恢复有效：再次确认仍拒绝，最近有效结果继续排除它，没有其他有效结果时
// 明确无结果。作废前取得的已确认记录保持取得时的内容。
func TestVoidedRecordMutationDoesNotRevive(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))
	mustSample(t, s, "S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	before, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	checkTwoItemSaved(t, before, "作废前确认")

	if _, err := s.Void("S1", "采样瓶破损"); err != nil {
		t.Fatalf("Void: %v", err)
	}

	checkVoided := func(smp Sample, label string) {
		t.Helper()
		if smp.Status != StatusVoided || smp.VoidReason != "采样瓶破损" {
			t.Fatalf("%s: 作废状态或原因被改动: %+v", label, smp)
		}
		// 原测量与原判定结果（含逐项依据和整份超标标记）保留
		if smp.ID != "S1" || smp.PointID != "P1" || !smp.SampledAt.Equal(at(10, 0)) {
			t.Fatalf("%s: 基本字段被改动: %+v", label, smp)
		}
		if !smp.Exceeded || len(smp.Measurements) != 2 || len(smp.Results) != 2 {
			t.Fatalf("%s: 原判定结果未保留: %+v", label, smp)
		}
		rs := map[string]ItemResult{}
		for _, r := range smp.Results {
			rs[r.Item] = r
		}
		if ph := rs["pH"]; ph.Value != 9 || ph.Limit != 8.0 || !ph.Exceeded {
			t.Fatalf("%s: pH 原判定未保留: %+v", label, ph)
		}
		if cod := rs["COD"]; cod.Value != 30 || cod.Limit != 30.0 || cod.Exceeded {
			t.Fatalf("%s: COD 原判定未保留: %+v", label, cod)
		}
	}

	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	checkVoided(list[0], "作废后按点查询")

	// 作废前取得的已确认记录保持取得时的内容，不随作废操作变化
	checkTwoItemSaved(t, before, "作废后先前取得的记录")

	// 调用方把查询得到的作废记录改回已确认、清空作废原因、伪造结论
	forged := list[0]
	forged.Status = StatusConfirmed
	forged.VoidReason = ""
	forged.Exceeded = false
	forged.Results[0].Exceeded = false

	// 保存记录不能恢复有效：再次确认仍拒绝
	if _, err := s.Confirm("S1"); !errors.Is(err, ErrVoided) {
		t.Fatalf("已作废样品再次确认应拒绝, got %v", err)
	}
	// 最近有效结果继续排除该样品；没有其他有效结果时明确无结果
	if latest, ok, err := s.LatestResult("P1"); err != nil || ok {
		t.Fatalf("作废后应无最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
	// 再次查询仍是作废记录，原测量、原判定与作废原因俱在
	list2, err := s.ListByPoint("P1")
	if err != nil || len(list2) != 1 {
		t.Fatalf("伪造后 ListByPoint: %+v err=%v", list2, err)
	}
	checkVoided(list2[0], "伪造后再次查询")
	// 正常重复作废（相同原因）仍返回保存的作废记录
	again, err := s.Void("S1", "采样瓶破损")
	if err != nil {
		t.Fatalf("重复作废: %v", err)
	}
	checkVoided(again, "重复作废返回值")
}
