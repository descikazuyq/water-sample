package water

import (
	"errors"
	"reflect"
	"testing"
)

// 本文件的测试只针对一件事：测量内容在“提交当时”就被固定为样品各自的快照。
// 调用方提交用的测量列表在录入成功之后可以继续编辑（改值、换项目名、复用同一
// 切片再录别的样品），这些本地编辑既不能顺着入参切片写回任何已保存样品，也不能
// 顺着成功返回的待判定记录写回数据存放。按采样点查看必须仍是各自提交时的项目
// 数量、名称、数值与首次录入排列；状态保持待判定，没有逐项判定依据，也没有整份
// 达标/超标结论。同编号重报沿用现有幂等与内容冲突规则；确认必须按各自保存的
// 原值取采样当时生效的上限逐项判定，调用方改过的项目和值不能混入判定，更不能
// 让确认为一个并未真正录入的项目报缺少限值。录入、重报、确认的公开入口与错误
// 含义均保持现状，本文件是对现有隔离行为的回归保障。

// setupIsolationStore 准备“同一点位两项上限在采样当时均已生效”的存放：
// pH 上限 8、COD 上限 30，均为 2026-09-01 生效。
func setupIsolationStore(t *testing.T) (*Store, string) {
	t.Helper()
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))
	return s, dir
}

// checkMeasurementSnapshot 锁定一份样品保存的测量快照：项目数量正确、
// 排列仍是首次录入的 pH、COD，名称与数值与提交当时一致。
func checkMeasurementSnapshot(t *testing.T, smp Sample, label string, want map[string]float64) {
	t.Helper()
	if len(smp.Measurements) != len(want) {
		t.Fatalf("%s: 测量项目数量被改动: got %+v", label, smp.Measurements)
	}
	gotOrder := make([]string, len(smp.Measurements))
	got := make(map[string]float64, len(smp.Measurements))
	for i, m := range smp.Measurements {
		gotOrder[i] = m.Item
		got[m.Item] = m.Value
	}
	if !reflect.DeepEqual(gotOrder, []string{"pH", "COD"}) {
		t.Fatalf("%s: 测量排列应保持首次录入的 pH、COD: got %v", label, gotOrder)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: 测量项目或数值被改动: got %v want %v", label, got, want)
	}
}

// checkPendingSnapshot 锁定按采样点查到的仍是待判定原样记录：
// 保存状态为待判定、没有任何逐项依据、整份超标标记是“无结论”的零值 false，
// 编号、采样点、采样时间与测量快照都与提交当时一致。
func checkPendingSnapshot(t *testing.T, smp Sample, label, id string, want map[string]float64) {
	t.Helper()
	if smp.ID != id || smp.PointID != "P1" || !smp.SampledAt.Equal(at(10, 0)) {
		t.Fatalf("%s: 样品归属或采样时间被改动: %+v", label, smp)
	}
	if smp.Status != StatusPending {
		t.Fatalf("%s: 本地编辑不能让记录提前成为已确认样品: %+v", label, smp)
	}
	if len(smp.Results) != 0 {
		t.Fatalf("%s: 待判定记录不能出现逐项判定依据: %+v", label, smp.Results)
	}
	if smp.Exceeded {
		t.Fatalf("%s: 本地编辑不能产生整份达标/超标结论: %+v", label, smp)
	}
	if smp.VoidReason != "" {
		t.Fatalf("%s: 作废原因被改动: %+v", label, smp)
	}
	checkMeasurementSnapshot(t, smp, label, want)
}

// 调用方手里只有一份测量列表（pH 9、COD 30），先用它录入样品甲，随后复用同一
// 切片改成 pH 7、COD 20 录入样品乙，之后再把列表中的项目名称与数值继续替换成
// 采样点上根本没有的项目。两份已保存样品必须各自固定在提交当时：甲不跟着变成
// 乙的数值，任何一次列表编辑都不写回。首次录入成功返回的样品同样是取得时的
// 副本。再篡改两份返回的待判定记录（项目、数值、状态、伪造逐项依据），按采样点
// 查看仍是原来的两份待判定样品，最近有效结果明确无结果。
//
// 同编号重报原始内容幂等返回原记录；交换两个项目的提交顺序不构成变化，排列仍以
// 首次录入为准；只改一个测量值仍按现有的 ErrSampleConflict 整体拒绝并返回空样品，
// 原记录不受影响。
func TestSubmitSnapshotsMeasurementsAwayFromCallerList(t *testing.T) {
	s, _ := setupIsolationStore(t)

	wantJia := map[string]float64{"pH": 9, "COD": 30}
	wantYi := map[string]float64{"pH": 7, "COD": 20}

	// 调用方手里只有一份测量列表：pH 9、COD 30。
	ms := []Measurement{{Item: "pH", Value: 9}, {Item: "COD", Value: 30}}

	// 用这份列表录入样品甲（S1）。variadic 直接传同一切片，存放若持有
	// 调用方底层数组，后续编辑就会悄悄改到甲——这正是要堵住的回归。
	jia, err := s.SubmitSample("S1", "P1", at(10, 0), ms...)
	if err != nil {
		t.Fatalf("录入样品甲: %v", err)
	}
	if jia.Status != StatusPending {
		t.Fatalf("首次录入应为待判定: %+v", jia)
	}
	checkMeasurementSnapshot(t, jia, "首次录入返回的甲", wantJia)

	// 复用同一份列表，只把数值改成 pH 7、COD 20，以不同编号录入样品乙（S2）。
	ms[0].Value = 7
	ms[1].Value = 20
	yi, err := s.SubmitSample("S2", "P1", at(10, 0), ms...)
	if err != nil {
		t.Fatalf("录入样品乙: %v", err)
	}
	checkMeasurementSnapshot(t, yi, "第二次录入返回的乙", wantYi)
	// 甲的返回记录是取得时的快照，不随原列表改值而变。
	checkMeasurementSnapshot(t, jia, "列表改值后甲的返回记录", wantJia)

	// 继续替换列表中的项目名称与测量值：两份已保存样品都不应受影响。
	ms[0].Item = "SS"
	ms[0].Value = 0.1
	ms[1].Item = "TN"
	ms[1].Value = 999
	checkMeasurementSnapshot(t, jia, "列表换名改值后甲的返回记录", wantJia)
	checkMeasurementSnapshot(t, yi, "列表换名改值后乙的返回记录", wantYi)

	// 保存记录两份独立：甲仍是 9/30，乙仍是 7/20；同一采样时刻按编号升序。
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListByPoint 应只有甲、乙两份: %+v err=%v", list, err)
	}
	if list[0].ID != "S1" || list[1].ID != "S2" {
		t.Fatalf("同一采样时刻应按编号升序排列: %+v", list)
	}
	checkPendingSnapshot(t, list[0], "列表编辑后保存的甲", "S1", wantJia)
	checkPendingSnapshot(t, list[1], "列表编辑后保存的乙", "S2", wantYi)

	// 篡改首次录入成功返回的待判定记录：项目名、测量值、状态、整份标记、
	// 伪造逐项依据全部改掉。
	jia.Measurements[0].Item = "HACK"
	jia.Measurements[0].Value = 123
	jia.Measurements[1].Value = 456
	jia.Status = StatusConfirmed
	jia.Exceeded = true
	jia.Results = []ItemResult{{
		Item: "HACK", Value: 123, Limit: 1, LimitEffective: at(2, 0), Exceeded: false,
	}}
	// 改甲的返回记录不能窜到乙的返回记录。
	checkMeasurementSnapshot(t, yi, "篡改甲的返回记录后乙的返回记录", wantYi)
	yi.Measurements[0].Value = 0
	yi.Measurements[1].Item = "HACK2"
	yi.Status = StatusConfirmed
	yi.Exceeded = true

	// 按采样点查看：仍是原来保存的两份待判定样品，项目数量、名称、数值和
	// 首次录入排列保持原样，没有逐项依据与整份结论。
	list2, err := s.ListByPoint("P1")
	if err != nil || len(list2) != 2 {
		t.Fatalf("篡改返回记录后 ListByPoint: %+v err=%v", list2, err)
	}
	checkPendingSnapshot(t, list2[0], "篡改返回记录后保存的甲", "S1", wantJia)
	checkPendingSnapshot(t, list2[1], "篡改返回记录后保存的乙", "S2", wantYi)
	// 两份都还待判定，最近有效结果必须明确无结果（而不是把伪造的 false/true 当结论）。
	if latest, ok, err := s.LatestResult("P1"); err != nil || ok {
		t.Fatalf("待判定样品不应成为最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}

	// 相同编号重报原始内容：幂等返回原记录，与按点查到的保存记录逐字段一致。
	again, err := s.SubmitSample("S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	if err != nil {
		t.Fatalf("重报原内容不应被拒绝: %v", err)
	}
	checkPendingSnapshot(t, again, "原内容重报返回", "S1", wantJia)
	if !reflect.DeepEqual(again, list2[0]) {
		t.Fatalf("重报原内容应返回同一份待判定记录:\n got %+v\nwant %+v", again, list2[0])
	}

	// 交换两个项目的提交顺序（叠加编号、采样点与项目名首尾空白）不构成变化，
	// 返回的仍是同一份记录，排列保持首次录入的 pH、COD。
	swapped, err := s.SubmitSample(" S1 ", " P1 ", at(10, 0),
		Measurement{Item: " COD ", Value: 30}, Measurement{Item: " pH ", Value: 9})
	if err != nil {
		t.Fatalf("换序重报不应被拒绝: %v", err)
	}
	checkPendingSnapshot(t, swapped, "换序重报返回", "S1", wantJia)
	if !reflect.DeepEqual(swapped, list2[0]) {
		t.Fatalf("换序重报应返回同一份记录且排列不变:\n got %+v\nwant %+v",
			swapped, list2[0])
	}

	// 只改一个测量值（pH 9 -> 8.5）：仍按现有的内容冲突错误整体拒绝，
	// 返回空样品，不能忽略 err 后把零值当成功记录。
	conflict, err := s.SubmitSample("S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 8.5}, Measurement{Item: "COD", Value: 30})
	if !errors.Is(err, ErrSampleConflict) {
		t.Fatalf("只改一个测量值重报应报样品内容冲突, got %v", err)
	}
	checkEmptySample(t, conflict, "改值重报")

	// 拒绝不留痕：两份样品仍是原样的待判定记录，重报不产生新记录。
	list3, err := s.ListByPoint("P1")
	if err != nil || len(list3) != 2 {
		t.Fatalf("冲突被拒后应仍只有甲、乙两份: %+v err=%v", list3, err)
	}
	checkPendingSnapshot(t, list3[0], "冲突被拒后保存的甲", "S1", wantJia)
	checkPendingSnapshot(t, list3[1], "冲突被拒后保存的乙", "S2", wantYi)
	if latest, ok, err := s.LatestResult("P1"); err != nil || ok {
		t.Fatalf("冲突被拒后仍应无最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
}

// 确认两份样品时，判定必须使用各自保存的原值，而不是调用方手里已经改过的列表
// 或返回记录：甲的 pH 9 大于上限 8 超标、COD 30 恰好等于上限 30 达标，整份超标；
// 乙的 pH 7、COD 20 两项均达标，整份达标。逐项结果带上对应原测量值、实际使用的
// 上限及其生效时间（8@2026-09-01、30@2026-09-01）。提交后列表被换成采样点上
// 没有限值的 SS、TN，也不能使确认为并未真正录入的项目报缺少限值。落盘重开后
// 保存快照与判定依据仍是各自提交时的内容。
func TestConfirmUsesSavedMeasurementsNotCallerEdits(t *testing.T) {
	s, dir := setupIsolationStore(t)

	// 与录入场景相同：复用一份列表录两份样品。
	ms := []Measurement{{Item: "pH", Value: 9}, {Item: "COD", Value: 30}}
	jia, err := s.SubmitSample("S1", "P1", at(10, 0), ms...)
	if err != nil {
		t.Fatalf("录入样品甲: %v", err)
	}
	ms[0].Value = 7
	ms[1].Value = 20
	yi, err := s.SubmitSample("S2", "P1", at(10, 0), ms...)
	if err != nil {
		t.Fatalf("录入样品乙: %v", err)
	}
	// 提交后继续把列表替换成采样点上根本没有限值的项目与任意数值。
	ms[0].Item, ms[0].Value = "SS", 0.1
	ms[1].Item, ms[1].Value = "TN", 999
	// 同时篡改两份成功返回的待判定记录。
	jia.Measurements[0].Item, jia.Measurements[0].Value = "HACK", 5
	jia.Measurements[1].Value = 6
	jia.Exceeded = true
	yi.Measurements[0].Value = 111
	yi.Measurements[1].Item = "HACK2"

	// checkResult 锁定一条逐项依据：原测量值、实际使用的上限、上限生效时间
	// 与单项结论逐字段一致。
	checkResult := func(r ItemResult, label, item string, value, limit float64, exceeded bool) {
		t.Helper()
		want := ItemResult{
			Item: item, Value: value, Limit: limit,
			LimitEffective: at(1, 0), Exceeded: exceeded,
		}
		if !reflect.DeepEqual(r, want) {
			t.Fatalf("%s: 逐项依据应使用保存的原值与采样当时的上限:\n got %+v\nwant %+v",
				label, r, want)
		}
	}

	// 正常确认甲：必须按保存的 pH/COD 找到上限并成功，调用方列表里的 SS/TN
	// 不能混入，也不能报“并未真正录入的项目缺少限值”。
	c1, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("确认甲必须按保存的 pH/COD 取上限，调用方改过的项目不应造成缺少限值: %v", err)
	}
	if c1.Status != StatusConfirmed || !c1.Exceeded {
		t.Fatalf("甲应为已确认且整份超标（pH 9 > 8）: %+v", c1)
	}
	checkMeasurementSnapshot(t, c1, "确认甲返回的测量", map[string]float64{"pH": 9, "COD": 30})
	if len(c1.Results) != 2 {
		t.Fatalf("甲应有两项逐项依据: %+v", c1.Results)
	}
	checkResult(c1.Results[0], "甲 pH 项", "pH", 9, 8.0, true)      // 9 > 8：超标
	checkResult(c1.Results[1], "甲 COD 项", "COD", 30, 30.0, false) // 30 == 30：达标

	// 正常确认乙：两项均不超标，整份达标；数值是乙提交时的 7/20，
	// 不能混入甲的 9/30。
	c2, err := s.Confirm("S2")
	if err != nil {
		t.Fatalf("确认乙: %v", err)
	}
	if c2.Status != StatusConfirmed || c2.Exceeded {
		t.Fatalf("乙应为已确认且整份达标: %+v", c2)
	}
	checkMeasurementSnapshot(t, c2, "确认乙返回的测量", map[string]float64{"pH": 7, "COD": 20})
	if len(c2.Results) != 2 {
		t.Fatalf("乙应有两项逐项依据: %+v", c2.Results)
	}
	checkResult(c2.Results[0], "乙 pH 项", "pH", 7, 8.0, false)
	checkResult(c2.Results[1], "乙 COD 项", "COD", 20, 30.0, false)

	// 按采样点查看：保存记录上的测量与逐项依据互不混拼。
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	if list[0].ID != "S1" || list[1].ID != "S2" {
		t.Fatalf("排列应为甲在前乙在后: %+v", list)
	}
	if !reflect.DeepEqual(list[0].Results, c1.Results) || !list[0].Exceeded {
		t.Fatalf("保存的甲与确认返回不一致: %+v", list[0])
	}
	if !reflect.DeepEqual(list[1].Results, c2.Results) || list[1].Exceeded {
		t.Fatalf("保存的乙与确认返回不一致: %+v", list[1])
	}
	// 同一采样时刻最近有效结果按编号取甲：整份超标的那份，依据同样来自保存快照。
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("最近有效结果应指向甲: %+v ok=%v err=%v", latest, ok, err)
	}
	checkResult(latest.Results[0], "最近结果 pH 项", "pH", 9, 8.0, true)
	checkResult(latest.Results[1], "最近结果 COD 项", "COD", 30, 30.0, false)

	// 关闭后重新打开：落盘的是提交当时的快照，调用方此后的全部本地编辑
	// （SS/TN、HACK 等）都不在记录里。
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("重新 Open: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })

	list2, err := reopened.ListByPoint("P1")
	if err != nil || len(list2) != 2 {
		t.Fatalf("重开后 ListByPoint: %+v err=%v", list2, err)
	}
	checkMeasurementSnapshot(t, list2[0], "重开后甲的测量", map[string]float64{"pH": 9, "COD": 30})
	checkMeasurementSnapshot(t, list2[1], "重开后乙的测量", map[string]float64{"pH": 7, "COD": 20})
	for i, smp := range list2 {
		if smp.Status != StatusConfirmed || len(smp.Results) != 2 {
			t.Fatalf("重开后样品 %d 状态或依据异常: %+v", i, smp)
		}
		for _, r := range smp.Results {
			if r.Item == "SS" || r.Item == "TN" || r.Item == "HACK" || r.Item == "HACK2" {
				t.Fatalf("调用方改过的项目名混入了已保存判定: %+v", r)
			}
		}
	}
	checkResult(list2[0].Results[0], "重开后甲 pH 项", "pH", 9, 8.0, true)
	checkResult(list2[0].Results[1], "重开后甲 COD 项", "COD", 30, 30.0, false)
	checkResult(list2[1].Results[0], "重开后乙 pH 项", "pH", 7, 8.0, false)
	checkResult(list2[1].Results[1], "重开后乙 COD 项", "COD", 20, 30.0, false)
	if list2[0].Exceeded != true || list2[1].Exceeded != false {
		t.Fatalf("重开后整份结论异常: 甲=%v 乙=%v", list2[0].Exceeded, list2[1].Exceeded)
	}
}
