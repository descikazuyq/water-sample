package water

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// 本文件的测试只针对一件事：测量内容在“提交当时”被固定下来。
// 调用方录入时传入的测量列表归调用方所有：样品成功录入后，调用方仍可以
// 在这份列表上改值、替换项目名称、追加项目，再拿去录入别的样品。每一份
// 已录入样品都必须独立保存各自提交当时的项目、数值与首次录入的排列，
// 甲不能因为同一份列表后来被改成乙的数值而跟着变；录入成功返回的待判定
// 记录也只是副本，本地编辑既不能写回保存记录，也不能让它提前成为
// 已确认样品或凭空产生整份结论。同编号重报、内容冲突拒绝与确认判定的
// 公开行为均保持现状，本文件是这些行为的回归保障。

// setupSharedListSamples 建立本文件共用的场景：同一采样点登记两项采样当时
// 已生效的上限（pH 8、COD 30，均自 09-01 00:00 UTC 生效）。调用方只维护
// 一份测量列表，先以编号 S1（样品甲）在 09-10 录入 pH 9、COD 30；随后
// 原地把这份列表改成 pH 7、COD 20，再以编号 S2（样品乙）在 09-11 录入。
// 返回两份样品提交当时的返回记录，以及调用方仍持有并可继续编辑的列表。
func setupSharedListSamples(t *testing.T) (s *Store, dir string, jia, yi Sample, ms []Measurement) {
	t.Helper()
	s, dir = open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))

	// 调用方全程只维护这一份测量列表，两次录入都把它原样以 ms... 传入。
	ms = []Measurement{
		{Item: "pH", Value: 9},
		{Item: "COD", Value: 30},
	}
	jia = mustSample(t, s, "S1", "P1", at(10, 0), ms...)

	// 复用同一份列表，原地改成乙的数值后再录入：若保存时借用了调用方的
	// 底层数组，甲在这里就会悄悄跟着变成 7、20。
	ms[0].Value = 7
	ms[1].Value = 20
	yi = mustSample(t, s, "S2", "P1", at(11, 0), ms...)
	return s, dir, jia, yi, ms
}

// checkPendingSnapshot 锁定一份待判定样品保存（或返回）的内容：
// 状态必须是待判定、没有逐项判定依据、没有整份超标/达标结论、没有作废
// 原因；测量项目数量、名称、数值与首次录入的排列（pH、COD）都必须与
// 提交当时一致。values 给出每个项目应有的测量值。
func checkPendingSnapshot(t *testing.T, smp Sample, id string, sampledAt time.Time, values map[string]float64, label string) {
	t.Helper()
	if smp.ID != id || smp.PointID != "P1" || smp.Status != StatusPending {
		t.Fatalf("%s: 编号、采样点或待判定状态被改动: %+v", label, smp)
	}
	if !smp.SampledAt.Equal(sampledAt) {
		t.Fatalf("%s: 采样时间被改动: %+v", label, smp)
	}
	if smp.VoidReason != "" {
		t.Fatalf("%s: 待判定样品不能出现作废原因: %+v", label, smp)
	}
	// 待判定就是待判定：本地编辑不能提前确认，也不能产生整份结论。
	if smp.Exceeded || len(smp.Results) != 0 {
		t.Fatalf("%s: 待判定样品不能带逐项依据或整份结论: %+v", label, smp)
	}
	if len(smp.Measurements) != len(values) {
		t.Fatalf("%s: 测量项目数量被改动: %+v", label, smp.Measurements)
	}
	// 排列必须仍是首次录入的 pH、COD，不能被后一次提交或本地编辑换序。
	gotOrder := make([]string, len(smp.Measurements))
	gotValues := make(map[string]float64, len(smp.Measurements))
	for i, m := range smp.Measurements {
		gotOrder[i] = m.Item
		gotValues[m.Item] = m.Value
	}
	wantOrder := []string{"pH", "COD"}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatalf("%s: 测量排列应保持首次录入的 pH、COD: %v", label, gotOrder)
	}
	for item, want := range values {
		if gotValues[item] != want {
			t.Fatalf("%s: 项目 %s 的测量值应固定为 %v: %+v", label, item, want, smp.Measurements)
		}
	}
}

// findSample 在按点查询结果里按编号取出一份样品。
func findSample(t *testing.T, list []Sample, id string) Sample {
	t.Helper()
	for _, smp := range list {
		if smp.ID == id {
			return smp
		}
	}
	t.Fatalf("样品 %q 不在按点查询结果中: %+v", id, list)
	return Sample{}
}

// 同一份调用方测量列表连续录入甲、乙两份样品：两份必须各自保存提交当时的
// 内容。之后调用方继续在原列表上替换项目名称、测量值并追加项目，既不能
// 改变任何一份已经保存的样品，也不能改变两次录入成功时返回的记录。
// 修改录入返回的待判定样品（状态、整份标记、伪造逐项依据、测量项目和值）
// 后按采样点查看，看到的仍是原来两份待判定样品，数量、名称、数值与首次
// 录入的排列不变，也不会冒出确认状态或整份达标结论，最近有效结果仍为空。
// 同编号重报原内容（含交换两个项目的提交顺序）返回原待判定记录；只改一个
// 测量值、或直接拿被污染的列表重报，都按现有的内容冲突整体拒绝并返回空
// 样品，原记录不受影响。
func TestSharedMeasurementListIsolationOnSubmit(t *testing.T) {
	s, _, jia, yi, ms := setupSharedListSamples(t)

	jiaValues := map[string]float64{"pH": 9, "COD": 30}
	yiValues := map[string]float64{"pH": 7, "COD": 20}
	checkPendingSnapshot(t, jia, "S1", at(10, 0), jiaValues, "甲首次录入返回")
	checkPendingSnapshot(t, yi, "S2", at(11, 0), yiValues, "乙首次录入返回")

	// 调用方继续在同一份列表上替换项目名称、测量值，并追加一个项目。
	ms[0] = Measurement{Item: "SS", Value: 1}
	ms[1].Item = "TN"
	ms[1].Value = 999
	ms = append(ms, Measurement{Item: "pH", Value: 0})

	// 按采样点查看：两份样品独立保存，乙采样更晚排在前面；各自的测量
	// 仍固定为提交当时的项目、数值与 pH、COD 排列。
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListByPoint 应只有甲、乙两份: %+v err=%v", list, err)
	}
	if list[0].ID != "S2" || list[1].ID != "S1" {
		t.Fatalf("排列应为较晚的乙在前、甲在后: %+v", list)
	}
	savedJia := findSample(t, list, "S1")
	savedYi := findSample(t, list, "S2")
	checkPendingSnapshot(t, savedJia, "S1", at(10, 0), jiaValues, "污染列表后保存的甲")
	checkPendingSnapshot(t, savedYi, "S2", at(11, 0), yiValues, "污染列表后保存的乙")
	// 两次录入成功返回的记录同样保持取得时的内容，不随原列表的编辑变化。
	checkPendingSnapshot(t, jia, "S1", at(10, 0), jiaValues, "污染列表后甲的首次返回")
	checkPendingSnapshot(t, yi, "S2", at(11, 0), yiValues, "污染列表后乙的首次返回")

	// 调用方肆意修改录入返回的待判定样品：伪造确认状态、整份达标结论、
	// 逐项依据、作废原因，并替换、追加测量项目。
	forge := func(smp Sample) {
		smp.Status = StatusConfirmed
		smp.Exceeded = true
		smp.VoidReason = "伪造原因"
		smp.Results = []ItemResult{
			{Item: "pH", Value: 1, Limit: 2, LimitEffective: at(2, 0), Exceeded: false},
		}
		smp.Measurements[0].Item = "SS"
		smp.Measurements[0].Value = 999
		smp.Measurements = append(smp.Measurements, Measurement{Item: "TN", Value: 1})
	}
	forge(jia)
	forge(yi)

	// 保存记录与成功返回记录相互隔离：按点查看仍是原来两份待判定样品，
	// 项目数量、名称、数值和首次录入的排列保持原样，本地伪造一律不写回。
	list2, err := s.ListByPoint("P1")
	if err != nil || len(list2) != 2 {
		t.Fatalf("伪造返回记录后 ListByPoint 应仍只有甲、乙两份: %+v err=%v", list2, err)
	}
	checkPendingSnapshot(t, findSample(t, list2, "S1"), "S1", at(10, 0), jiaValues, "伪造返回记录后的甲")
	checkPendingSnapshot(t, findSample(t, list2, "S2"), "S2", at(11, 0), yiValues, "伪造返回记录后的乙")
	// 两份都仍待判定，该点没有已确认且未作废的样品：明确无最近有效结果，
	// 本地伪造的“达标”不能解释成结论。
	if latest, ok, err := s.LatestResult("P1"); err != nil || ok {
		t.Fatalf("两份样品均待判定时应无最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}

	// 同编号重报原始内容：返回原待判定记录，与保存记录逐字段一致。
	again, err := s.SubmitSample("S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	if err != nil {
		t.Fatalf("重报甲的原内容不应被拒绝: %v", err)
	}
	checkPendingSnapshot(t, again, "S1", at(10, 0), jiaValues, "原内容重报甲")
	if !reflect.DeepEqual(again, savedJia) {
		t.Fatalf("原内容重报应返回原待判定记录:\n got %+v\nwant %+v", again, savedJia)
	}
	// 交换两个项目的提交顺序不构成变化，返回记录的排列仍是首次录入的 pH、COD。
	swapped, err := s.SubmitSample(" S1 ", " P1 ", at(10, 0),
		Measurement{Item: "COD", Value: 30}, Measurement{Item: " pH ", Value: 9})
	if err != nil {
		t.Fatalf("换序重报甲不应被拒绝: %v", err)
	}
	checkPendingSnapshot(t, swapped, "S1", at(10, 0), jiaValues, "换序重报甲")
	if !reflect.DeepEqual(swapped, savedJia) {
		t.Fatalf("换序重报应返回同一份待判定记录:\n got %+v\nwant %+v", swapped, savedJia)
	}

	// 只改一个测量值：仍按现有的内容冲突错误整体拒绝并返回空样品。
	conflict, err := s.SubmitSample("S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 8}, Measurement{Item: "COD", Value: 30})
	if !errors.Is(err, ErrSampleConflict) {
		t.Fatalf("只改 pH 一个测量值重报应按内容冲突拒绝, got %v", err)
	}
	checkEmptySample(t, conflict, "改值重报甲")
	// 直接拿调用方后来污染成 SS/TN/pH 的列表重报同样是冲突，不能被当成
	// 甲后来真正录入过的内容。
	conflictPolluted, err := s.SubmitSample("S1", "P1", at(10, 0), ms...)
	if !errors.Is(err, ErrSampleConflict) {
		t.Fatalf("拿污染后的列表重报甲应按内容冲突拒绝, got %v", err)
	}
	checkEmptySample(t, conflictPolluted, "污染列表重报甲")

	// 拒绝不留痕：两份保存记录都还是各自提交当时的待判定内容。
	list3, err := s.ListByPoint("P1")
	if err != nil || len(list3) != 2 {
		t.Fatalf("冲突拒绝后 ListByPoint 应仍只有甲、乙两份: %+v err=%v", list3, err)
	}
	checkPendingSnapshot(t, findSample(t, list3, "S1"), "S1", at(10, 0), jiaValues, "冲突拒绝后的甲")
	checkPendingSnapshot(t, findSample(t, list3, "S2"), "S2", at(11, 0), yiValues, "冲突拒绝后的乙")
}

// checkYiConfirmed 核对样品乙确认后的完整结论：两项均达标，整份不超标；
// 逐项结果按首次录入的 pH、COD 排列，各自带上乙提交当时的原测量值
// （pH 7、COD 20）、采样当时实际使用的上限（8、30）及其生效时间 09-01。
func checkYiConfirmed(t *testing.T, smp Sample, label string) {
	t.Helper()
	if smp.ID != "S2" || smp.PointID != "P1" || smp.Status != StatusConfirmed || smp.VoidReason != "" {
		t.Fatalf("%s: 乙的编号、采样点或状态被改动: %+v", label, smp)
	}
	if !smp.SampledAt.Equal(at(11, 0)) {
		t.Fatalf("%s: 乙的采样时间被改动: %+v", label, smp)
	}
	if smp.Exceeded {
		t.Fatalf("%s: 乙的两项均达标，整份不应超标: %+v", label, smp)
	}
	if len(smp.Measurements) != 2 || len(smp.Results) != 2 {
		t.Fatalf("%s: 乙的测量或逐项结果数量被改动: %+v", label, smp)
	}
	gotM := []string{smp.Measurements[0].Item, smp.Measurements[1].Item}
	if !reflect.DeepEqual(gotM, []string{"pH", "COD"}) {
		t.Fatalf("%s: 乙的测量排列应保持首次录入的 pH、COD: %v", label, gotM)
	}
	gotR := []string{smp.Results[0].Item, smp.Results[1].Item}
	if !reflect.DeepEqual(gotR, []string{"pH", "COD"}) {
		t.Fatalf("%s: 乙的逐项结果排列应随首次录入的 pH、COD: %v", label, gotR)
	}
	ph, cod := smp.Results[0], smp.Results[1]
	if ph.Item != "pH" || ph.Value != 7 || ph.Limit != 8.0 || !ph.LimitEffective.Equal(at(1, 0)) || ph.Exceeded {
		t.Fatalf("%s: 乙的 pH 应按原值 7 ≤ 8@09-01 达标: %+v", label, ph)
	}
	if cod.Item != "COD" || cod.Value != 20 || cod.Limit != 30.0 || !cod.LimitEffective.Equal(at(1, 0)) || cod.Exceeded {
		t.Fatalf("%s: 乙的 COD 应按原值 20 ≤ 30@09-01 达标: %+v", label, cod)
	}
}

// 正常确认甲、乙时，判定必须使用各自保存的原测量值：甲 pH 9 > 8 超标、
// COD 30 恰好等于上限 30 达标，整份超标；乙 pH 7、COD 20 两项均达标。
// 调用方录入后把共享列表改成从未登记限值的项目（SS、TN），并就地伪造了
// 两份录入返回记录里的项目和数值——这些编辑不能混入判定，不能让确认因
// 一个并未真正录入的项目而报缺少限值，也不能顶替逐项结果携带的原值、
// 实际使用的上限及其生效时间。关闭重开后，两份快照结论仍随落盘保持。
func TestConfirmUsesSavedSnapshotAfterCallerEdits(t *testing.T) {
	s, dir, jia, yi, ms := setupSharedListSamples(t)

	// 把共享列表改成两个从未登记上限的项目和离谱数值。
	ms[0].Item = "SS"
	ms[0].Value = 1
	ms[1].Item = "TN"
	ms[1].Value = 999
	// 同时伪造两份录入返回记录里的测量项目与数值。
	jia.Measurements[0].Item = "SS"
	jia.Measurements[0].Value = 1
	jia.Measurements[1].Value = 999
	yi.Measurements[0].Item = "TN"
	yi.Measurements[0].Value = 999

	// 确认甲：必须使用保存的 pH 9、COD 30，而不是 SS/TN；SS/TN 没有
	// 登记限值，若调用方编辑混入，这里会错误地报缺少适用上限。
	confJia, err := s.Confirm("S1")
	if errors.Is(err, ErrMissingLimit) {
		t.Fatalf("不能因调用方伪造的、并未真正录入的项目而报缺少限值: %v", err)
	}
	if err != nil {
		t.Fatalf("Confirm 甲: %v", err)
	}
	// 复用既有核对：S1/P1、09-10 采样，pH 9 > 8@09-01 超标、
	// COD 30 == 30@09-01 达标、整份超标，逐项依据与排列完整。
	checkTwoItemSaved(t, confJia, "甲确认返回")

	// 确认乙：两项均按乙保存的原值达标，整份不超标。
	confYi, err := s.Confirm("S2")
	if errors.Is(err, ErrMissingLimit) {
		t.Fatalf("乙的确认不能被调用方伪造的项目带偏成缺少限值: %v", err)
	}
	if err != nil {
		t.Fatalf("Confirm 乙: %v", err)
	}
	checkYiConfirmed(t, confYi, "乙确认返回")

	// 按点查看与最近有效结果使用的都是保存快照：较晚的乙是最近有效结果。
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListByPoint 应仍只有甲、乙两份: %+v err=%v", list, err)
	}
	checkTwoItemSaved(t, findSample(t, list, "S1"), "按点查看中的甲")
	checkYiConfirmed(t, findSample(t, list, "S2"), "按点查看中的乙")
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S2" {
		t.Fatalf("最近有效结果应指向较晚且达标的乙: %+v ok=%v err=%v", latest, ok, err)
	}
	checkYiConfirmed(t, latest, "最近有效结果中的乙")

	// 关闭重开：提交当时固定的测量与判定所用上限、生效时间随落盘保持，
	// 与调用方进程里那份已被改得面目全非的列表再无任何关系。
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
		t.Fatalf("重开后 ListByPoint 应仍只有甲、乙两份: %+v err=%v", list2, err)
	}
	checkTwoItemSaved(t, findSample(t, list2, "S1"), "重开后按点查看中的甲")
	checkYiConfirmed(t, findSample(t, list2, "S2"), "重开后按点查看中的乙")
}
