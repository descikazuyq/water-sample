package water

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// 本文件只围绕“对已经确认的样品重复录入”补回归保障：
// 同编号、同内容（项目排列、首尾空白、采样时间的时区写法都不算内容变化）的
// 再次提交必须返回首次录入并已确认的原记录——不能当成新样品，不能变回待判定，
// 不能采用确认之后才补录、但采样时刻同样适用的新版限值重新计算，
// 已保存的逐项测量值、所用上限、生效时间与单项/整份结论（含达标项依据）都要原样保留。
// 内容冲突或同份提交内项目重复仍走现有的样品内容冲突 / 项目重复错误整体拒绝，
// 返回空样品且不动原记录与查询结果；拒绝之后再提交合法原内容仍返回原已确认记录。
// 录入、查询入口与错误含义全部沿用现状。

// setupConfirmedS1 准备任务描述里的那份已确认样品：
// 采样点 P1 的 S1 于 2026-09-10 00:00 UTC 采样，pH 9 采用 8@09-01 上限判超标，
// COD 30 采用 30@09-01 上限（恰好等于）判达标，整份样品超标并已确认。
func setupConfirmedS1(t *testing.T) (*Store, string) {
	t.Helper()
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))
	mustSample(t, s, "S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	if _, err := s.Confirm("S1"); err != nil {
		t.Fatalf("准备数据：Confirm S1 失败: %v", err)
	}
	return s, dir
}

// checkConfirmedS1 完整核对重报/查询/重复确认拿到的 S1 必须保持的已保存内容。
// 重点保护已经保存的判定依据：pH 仍用 8@09-01 判超标，绝不能换成确认后补录的
// 10@09-09；COD 30 == 30@09-01 达标的依据不能丢；整份仍超标。
// 项目排列必须保持首次录入的 pH、COD 顺序，不能被后一次提交的排列替换。
func checkConfirmedS1(t *testing.T, smp Sample, label string) {
	t.Helper()
	if smp.ID != "S1" || smp.PointID != "P1" {
		t.Fatalf("%s: 编号或采样点被改动: %+v", label, smp)
	}
	// 不能变回待判定，也不能变成作废。
	if smp.Status != StatusConfirmed {
		t.Fatalf("%s: 重报不得把已确认样品变成状态 %q: %+v", label, smp.Status, smp)
	}
	if smp.VoidReason != "" {
		t.Fatalf("%s: 重报不应附带作废原因: %+v", label, smp)
	}
	// 换时区写法只认同一真实瞬间，保存的采样瞬间不变。
	if !smp.SampledAt.Equal(at(10, 0)) {
		t.Fatalf("%s: 采样瞬间应为 %s，实际 %s", label, at(10, 0), smp.SampledAt)
	}
	if !smp.Exceeded {
		t.Fatalf("%s: 整份结论应保持超标（pH 9 > 8@09-01），不能按补录的 10@09-09 重算成达标: %+v",
			label, smp)
	}
	if len(smp.Measurements) != 2 || len(smp.Results) != 2 {
		t.Fatalf("%s: 测量项目或逐项结果数量被改动: %+v", label, smp)
	}
	// 首次录入顺序是 pH 在前、COD 在后；后一次即使调换顺序提交，
	// 返回记录仍保持首次录入与确认时的排列。
	wantOrder := []string{"pH", "COD"}
	for i, item := range wantOrder {
		if smp.Measurements[i].Item != item {
			t.Fatalf("%s: 测量排列被后一次提交替换，第 %d 项应为 %s: %+v",
				label, i+1, item, smp.Measurements)
		}
		if smp.Results[i].Item != item {
			t.Fatalf("%s: 逐项结果排列被后一次提交替换，第 %d 项应为 %s: %+v",
				label, i+1, item, smp.Results)
		}
	}
	ph, cod := smp.Results[0], smp.Results[1]
	if ph.Value != 9 || ph.Limit != 8.0 || !ph.LimitEffective.Equal(at(1, 0)) || !ph.Exceeded {
		t.Fatalf("%s: pH 必须保持 9 > 8@09-01 超标，不能采用确认后补录的 10@09-09: %+v",
			label, ph)
	}
	if cod.Value != 30 || cod.Limit != 30.0 || !cod.LimitEffective.Equal(at(1, 0)) || cod.Exceeded {
		t.Fatalf("%s: COD 必须保持 30 == 30@09-01 达标，达标项依据不能丢失: %+v",
			label, cod)
	}
}

// 确认后为 pH 补录一版采样时刻同样适用的新上限（10@09-09），随后带着各种
// “不算内容变化”的差别重复录入原两个测量值：交换项目顺序、编号/采样点/项目名
// 带首尾空白、采样时间换时区表示同一瞬间。返回的必须仍是原来的已确认样品：
// 不新建记录、不退回待判定、不按新限值重算（pH 仍 8@09-01 超标，整份仍超标），
// COD 的达标依据完整，排列保持首次录入顺序。按采样点查看 S1 只出现一次，
// 最近有效结果仍是这份超标样品，且测量值、所用限值和生效时间与重报返回完全一致。
func TestConfirmedResubmitKeepsSavedJudgmentBasis(t *testing.T) {
	s, _ := setupConfirmedS1(t)

	// 确认之后补录 pH 新版上限：2026-09-09 生效，对 09-10 的采样时刻已经适用。
	// 若重报错误地重新判定，pH 9 ≤ 10 会变成达标、整份也变达标——这正是要防住的。
	mustLimit(t, s, "P1", "pH", 10.0, at(9, 0))

	// 同一瞬间的北京时间写法：2026-09-10 08:00 +08:00 == 2026-09-10 00:00 UTC。
	beijing := time.FixedZone("CST", 8*3600)
	sampledBeijing := time.Date(2026, 9, 10, 8, 0, 0, 0, beijing)
	if !sampledBeijing.Equal(at(10, 0)) {
		t.Fatalf("测试前提：两个时间应为同一瞬间: %s vs %s", sampledBeijing, at(10, 0))
	}

	// 重报：编号、采样点编号、项目名称带首尾空白；项目顺序换成 COD 在前；
	// 采样时间换时区表示。这些差别都不构成内容变化。
	resubmitted, err := s.SubmitSample("  S1  ", "  P1  ", sampledBeijing,
		Measurement{Item: "  COD  ", Value: 30}, Measurement{Item: " pH ", Value: 9})
	if err != nil {
		t.Fatalf("重复录入原内容应幂等成功返回原记录: %v", err)
	}
	checkConfirmedS1(t, resubmitted, "重报返回值")

	// 重复确认同样只返回已保存结果，不借重报机会按 10@09-09 重算。
	reconfirmed, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("重报后重复确认: %v", err)
	}
	checkConfirmedS1(t, reconfirmed, "重报后重复确认")

	// 按采样点查看：S1 只出现一次，仍是原已确认的超标样品，依据不重算、不缺项。
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("按点查看应只有一份 S1，不能把重报当成新样品: %+v err=%v", list, err)
	}
	checkConfirmedS1(t, list[0], "重报后按点查询")

	// 该点没有其他样品，最近有效结果仍是这份已确认的超标样品。
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("最近有效结果应仍指向 S1: %+v ok=%v err=%v", latest, ok, err)
	}
	checkConfirmedS1(t, latest, "重报后最近有效结果")

	// 重报返回、按点查询、最近有效结果三者的测量值、所用限值与生效时间必须一致。
	if !reflect.DeepEqual(resubmitted, list[0]) {
		t.Fatalf("重报返回与按点查询不一致:\n返回 %+v\n查询 %+v", resubmitted, list[0])
	}
	if !reflect.DeepEqual(resubmitted, latest) {
		t.Fatalf("重报返回与最近有效结果不一致:\n返回 %+v\n最近 %+v", resubmitted, latest)
	}
}

// 两类重报必须沿用现有错误整体拒绝，返回空样品，原记录和查询结果不受影响；
// 拒绝之后再提交合法原内容，仍能拿回原来的已确认记录。
func TestConfirmedResubmitRejectionsDoNotChangeSavedRecord(t *testing.T) {
	s, _ := setupConfirmedS1(t)
	// 同样先补录采样时刻已适用的新上限，证明下面的拒绝与限值无关，原依据也不被重算。
	mustLimit(t, s, "P1", "pH", 10.0, at(9, 0))

	beijing := time.FixedZone("CST", 8*3600)
	sampledBeijing := time.Date(2026, 9, 10, 8, 0, 0, 0, beijing)

	// 原记录与查询结果应始终保持的状态：唯一一份 S1，内容为首次确认保存的结论。
	assertOriginalIntact := func(label string) {
		t.Helper()
		list, err := s.ListByPoint("P1")
		if err != nil || len(list) != 1 || list[0].ID != "S1" {
			t.Fatalf("%s: 按点查看应仍只有一份 S1: %+v err=%v", label, list, err)
		}
		checkConfirmedS1(t, list[0], label+"-按点查询")
		latest, ok, err := s.LatestResult("P1")
		if err != nil || !ok {
			t.Fatalf("%s: 最近有效结果应仍指向 S1: %+v ok=%v err=%v", label, latest, ok, err)
		}
		checkConfirmedS1(t, latest, label+"-最近有效结果")
	}

	// 1) 重报时仅把 COD 改成另一个有限数（30 → 29）：以现有的样品内容冲突错误
	//    整体拒绝，返回空样品，原记录不动。
	conflict := Sample{}
	conflict, err := s.SubmitSample("S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 29})
	if !errors.Is(err, ErrSampleConflict) {
		t.Fatalf("COD 测量值变化应以样品内容冲突拒绝，got %v", err)
	}
	if conflict.ID != "" || conflict.Status != "" || conflict.Exceeded || len(conflict.Results) != 0 {
		t.Fatalf("冲突拒绝应返回空样品，不能返回带编号或结论的记录: %+v", conflict)
	}
	assertOriginalIntact("COD 冲突拒绝后")

	// 2) 同一份重报里两个项目去掉首尾空白后同名（" pH " 与 "pH"），即使值相同，
	//    也必须以现有的项目重复错误拒绝，不能合并成一个项目后视为内容相同。
	dup, err := s.SubmitSample("S1", "P1", at(10, 0),
		Measurement{Item: " pH ", Value: 9}, Measurement{Item: "pH", Value: 9},
		Measurement{Item: "COD", Value: 30})
	if !errors.Is(err, ErrDuplicateItem) {
		t.Fatalf("同份提交项目去空白后同名应以项目重复错误拒绝，got %v", err)
	}
	if dup.ID != "" || dup.Status != "" || dup.Exceeded || len(dup.Results) != 0 {
		t.Fatalf("项目重复拒绝应返回空样品: %+v", dup)
	}
	assertOriginalIntact("项目重复拒绝后")

	// 3) 两种拒绝之后，再次提交合法的原内容（仍可换序、带空白、换时区表示），
	//    必须返回原来的已确认记录，之前的拒绝不留任何影响。
	again, err := s.SubmitSample("  S1  ", " P1 ", sampledBeijing,
		Measurement{Item: " COD ", Value: 30}, Measurement{Item: "pH", Value: 9})
	if err != nil {
		t.Fatalf("拒绝后重报合法原内容应返回原记录: %v", err)
	}
	checkConfirmedS1(t, again, "拒绝后合法重报")
}

// 关闭后重新打开，已确认样品的重报保障继续成立：补录新限值后重开，
// 重复录入原内容仍返回落盘保存的原结论，不按新限值重算，查询结果一致且只有一份。
func TestConfirmedResubmitKeepsSavedBasisAfterReopen(t *testing.T) {
	s, dir := setupConfirmedS1(t)
	mustLimit(t, s, "P1", "pH", 10.0, at(9, 0))
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("重新打开数据存放: %v", err)
	}
	t.Cleanup(func() { s2.Close() })

	beijing := time.FixedZone("CST", 8*3600)
	sampledBeijing := time.Date(2026, 9, 10, 8, 0, 0, 0, beijing)
	got, err := s2.SubmitSample(" S1 ", " P1 ", sampledBeijing,
		Measurement{Item: " COD ", Value: 30}, Measurement{Item: "pH", Value: 9})
	if err != nil {
		t.Fatalf("重开后重复录入原内容应返回原记录: %v", err)
	}
	checkConfirmedS1(t, got, "重开后重报返回值")

	list, err := s2.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("重开后按点查看应只有一份 S1: %+v err=%v", list, err)
	}
	checkConfirmedS1(t, list[0], "重开后按点查询")
	latest, ok, err := s2.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("重开后最近有效结果应仍指向 S1: %+v ok=%v err=%v", latest, ok, err)
	}
	checkConfirmedS1(t, latest, "重开后最近有效结果")

	// 重开后冲突与项目重复错误含义不变，拒绝后合法重报仍返回原记录。
	if _, err := s2.SubmitSample("S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 29}); !errors.Is(err, ErrSampleConflict) {
		t.Fatalf("重开后 COD 变化仍应以冲突拒绝，got %v", err)
	}
	if _, err := s2.SubmitSample("S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: " pH ", Value: 9}); !errors.Is(err, ErrDuplicateItem) {
		t.Fatalf("重开后同份提交项目同名仍应以项目重复拒绝，got %v", err)
	}
	recovered, err := s2.SubmitSample("S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	if err != nil {
		t.Fatalf("重开后拒绝之后合法重报应返回原记录: %v", err)
	}
	checkConfirmedS1(t, recovered, "重开后拒绝后合法重报")
}
