package water

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// 本文件的测试只针对一件事：已确认样品被同编号、同内容重复录入（重报）时，
// 返回的必须是首次录入并已保存判定依据的那条记录——确认之后补录、且已适用
// 于采样时刻的新版限值不能触发重新判定，状态不能退回待判定，首次录入的
// 项目排列不能被后一次提交的排列替换，另一项目已保存的依据也不能丢。
// 内容冲突与同份提交内项目重复沿用现有的 ErrSampleConflict、ErrDuplicateItem
// 整体拒绝并返回空样品；拒绝不留痕，之后提交合法原内容仍返回原已确认记录。
// 录入、查询入口与错误含义均保持现状。

// checkConfirmedResubmit 锁定重报返回的就是首次确认时保存的记录。
// checkTwoItemSaved 已覆盖：编号 S1、采样点 P1、已确认状态、采样时间、
// pH 9 > 8@09-01 超标、COD 30 == 30@09-01 达标、整份超标；这里再锁定
// Measurements 与 Results 的项目排列必须是首次录入的 pH、COD 顺序。
func checkConfirmedResubmit(t *testing.T, smp Sample, label string) {
	t.Helper()
	checkTwoItemSaved(t, smp, label)
	want := []string{"pH", "COD"}
	gotM := make([]string, len(smp.Measurements))
	for i, m := range smp.Measurements {
		gotM[i] = m.Item
	}
	if !reflect.DeepEqual(gotM, want) {
		t.Fatalf("%s: 测量排列应保持首次录入的 pH、COD，不能用后一次提交的排列替换: %v",
			label, gotM)
	}
	gotR := make([]string, len(smp.Results))
	for i, r := range smp.Results {
		gotR[i] = r.Item
	}
	if !reflect.DeepEqual(gotR, want) {
		t.Fatalf("%s: 逐项结果排列应保持首次录入的 pH、COD: %v", label, gotR)
	}
}

// checkEmptySample 锁定被拒绝的提交返回空样品：连编号和状态都是零值，
// 调用方不能忽略 err 后把它当成新录入或已确认的记录使用。
func checkEmptySample(t *testing.T, smp Sample, label string) {
	t.Helper()
	if smp.ID != "" || smp.PointID != "" || smp.Status != "" ||
		len(smp.Measurements) != 0 || len(smp.Results) != 0 || smp.Exceeded ||
		smp.VoidReason != "" || !smp.SampledAt.IsZero() {
		t.Fatalf("%s: 被拒绝时应返回空样品，实际为: %+v", label, smp)
	}
}

// 已确认样品 S1（pH 9 > 8@09-01 超标，COD 30 == 30@09-01 达标，整份超标）
// 确认后补录一版 09-09 生效的 pH 上限 10：它对 09-10 的采样时刻已经适用，
// 若重新判定 pH 9 < 10 会变成达标、整份也变达标。随后重复录入原来的两个
// 测量值，返回的仍必须是原已确认样品：pH 所用上限仍为 8、生效时间仍为
// 09-01，各项与整份结论保持原样，不能变回待判定，也不能丢掉 COD 的依据。
//
// 交换项目顺序、编号/采样点/项目名带首尾空白、采样时间换另一时区表示同一
// 瞬间都不构成内容变化；每次成功返回都与首次确认返回逐字段一致，并保持
// 首次录入的项目排列。按采样点查看 S1 只出现一次；该点没有其他样品时，
// 最近有效结果仍是这份已确认的超标样品，测量值、所用限值和生效时间与
// 重报返回一致。重报是幂等返回，不写盘；关闭后重新打开，保存的依据不变，
// 在新进程里重报原内容仍返回同一条记录。
func TestResubmitConfirmedKeepsSavedBasis(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))
	first := mustSample(t, s, "S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	if first.Status != StatusPending || len(first.Results) != 0 {
		t.Fatalf("首次录入应为无判定依据的待判定样品: %+v", first)
	}
	confirmed, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("Confirm S1: %v", err)
	}
	checkConfirmedResubmit(t, confirmed, "首次确认")

	// 确认后补录新版 pH 上限：10@09-09 对 09-10 的采样已经生效，
	// 但它只能被此后“新”的判定采用，不能回头重算这份已确认样品。
	mustLimit(t, s, "P1", "pH", 10.0, at(9, 0))

	cst := time.FixedZone("CST", 8*3600) // 2026-09-10 08:00 +08:00 == 00:00 UTC
	cases := []struct {
		name      string
		id        string
		point     string
		sampledAt time.Time
		ms        []Measurement
	}{
		{
			name:      "交换两个项目的提交顺序",
			id:        "S1",
			point:     "P1",
			sampledAt: at(10, 0),
			ms: []Measurement{
				{Item: "COD", Value: 30},
				{Item: "pH", Value: 9},
			},
		},
		{
			name:      "编号带首尾空白",
			id:        "  S1  ",
			point:     "P1",
			sampledAt: at(10, 0),
			ms: []Measurement{
				{Item: "pH", Value: 9},
				{Item: "COD", Value: 30},
			},
		},
		{
			name:      "采样点编号与项目名称带首尾空白",
			id:        "S1",
			point:     "  P1 ",
			sampledAt: at(10, 0),
			ms: []Measurement{
				{Item: "  pH ", Value: 9},
				{Item: " COD ", Value: 30},
			},
		},
		{
			name:      "采样时间换成另一时区对同一瞬间的表示",
			id:        "S1",
			point:     "P1",
			sampledAt: time.Date(2026, 9, 10, 8, 0, 0, 0, cst),
			ms: []Measurement{
				{Item: "pH", Value: 9},
				{Item: "COD", Value: 30},
			},
		},
		{
			name:      "顺序空白时区差别叠加",
			id:        " S1 ",
			point:     " P1  ",
			sampledAt: time.Date(2026, 9, 10, 8, 0, 0, 0, cst),
			ms: []Measurement{
				{Item: " COD ", Value: 30},
				{Item: "  pH  ", Value: 9},
			},
		},
	}
	for _, c := range cases {
		got, err := s.SubmitSample(c.id, c.point, c.sampledAt, c.ms...)
		if err != nil {
			t.Fatalf("重报原内容（%s）不应被拒绝: %v", c.name, err)
		}
		checkConfirmedResubmit(t, got, "重报（"+c.name+"）")
		// 不能当成新样品，也不能用后一次内容重算：与首次确认返回逐字段一致。
		if !reflect.DeepEqual(got, confirmed) {
			t.Fatalf("重报（%s）应原样返回首次确认的记录:\n got %+v\nwant %+v",
				c.name, got, confirmed)
		}
	}

	// 按采样点查看：重报不产生新样品，S1 只出现一次且内容与重报返回一致。
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByPoint 应只有 S1 一份: %+v err=%v", list, err)
	}
	checkConfirmedResubmit(t, list[0], "重报后按点查询")
	if !reflect.DeepEqual(list[0], confirmed) {
		t.Fatalf("按点查询与首次确认记录不一致:\n got %+v\nwant %+v", list[0], confirmed)
	}

	// 该点没有其他样品时，最近有效结果仍是这份已确认的超标样品，
	// 测量值、所用限值、生效时间与重报返回完全一致。
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("最近有效结果应仍指向 S1: %+v ok=%v err=%v", latest, ok, err)
	}
	checkConfirmedResubmit(t, latest, "重报后最近有效结果")
	if !reflect.DeepEqual(latest, confirmed) {
		t.Fatalf("最近有效结果与首次确认记录不一致:\n got %+v\nwant %+v", latest, confirmed)
	}

	// 幂等重报不写盘：关闭后重新打开同一目录，已保存的判定依据原样保留。
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("重新 Open: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })

	list2, err := reopened.ListByPoint("P1")
	if err != nil || len(list2) != 1 {
		t.Fatalf("重开后 ListByPoint 应只有 S1 一份: %+v err=%v", list2, err)
	}
	checkConfirmedResubmit(t, list2[0], "重开后按点查询")
	latest2, ok, err := reopened.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("重开后最近有效结果应仍指向 S1: %+v ok=%v err=%v", latest2, ok, err)
	}
	checkConfirmedResubmit(t, latest2, "重开后最近有效结果")

	// 新进程里重报原内容（换序、空白、另一时区）仍返回同一条已确认记录，
	// 不会因补录的 10@09-09 重新判定，也不会产生第二份样品。
	again, err := reopened.SubmitSample(" S1 ", " P1 ",
		time.Date(2026, 9, 10, 8, 0, 0, 0, cst),
		Measurement{Item: "COD", Value: 30}, Measurement{Item: " pH ", Value: 9})
	if err != nil {
		t.Fatalf("重开后重报原内容: %v", err)
	}
	checkConfirmedResubmit(t, again, "重开后重报")
	if !reflect.DeepEqual(again, confirmed) {
		t.Fatalf("重开后重报应返回原已确认记录:\n got %+v\nwant %+v", again, confirmed)
	}
	list3, err := reopened.ListByPoint("P1")
	if err != nil || len(list3) != 1 {
		t.Fatalf("重开后重报不应产生新样品: %+v err=%v", list3, err)
	}
}

// 重报已确认样品时，仅把 COD 的测量值改成另一个有限数，属于样品内容冲突，
// 必须以现有的 ErrSampleConflict 整体拒绝并返回空样品；同一份重报里出现两个
// 去掉首尾空白后名称相同的项目（即使值相同），必须以现有的
// ErrDuplicateItem 拒绝，不能把两个同名项目合并后视为内容相同而幂等返回。
// 两种拒绝都不留痕：原记录的状态、测量、逐项依据与整份结论不变，按点查询
// 仍是唯一一份，最近有效结果仍指向它。拒绝之后再次提交合法的原内容
// （可带换序、空白、另一时区的等价差别），仍能返回原来的已确认记录。
func TestResubmitConfirmedRejectionsKeepRecord(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))
	mustSample(t, s, "S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	confirmed, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("Confirm S1: %v", err)
	}
	// 同样先补录一版已适用的新 pH 上限，确认拒绝路径也不会顺手重算原记录。
	mustLimit(t, s, "P1", "pH", 10.0, at(9, 0))

	// 每次被拒绝后，列表与最近有效结果都必须仍是原已确认记录，且只有一份。
	assertUntouched := func(label string) {
		t.Helper()
		list, err := s.ListByPoint("P1")
		if err != nil || len(list) != 1 {
			t.Fatalf("%s: ListByPoint 应仍只有 S1 一份: %+v err=%v", label, list, err)
		}
		checkConfirmedResubmit(t, list[0], label+"按点查询")
		latest, ok, err := s.LatestResult("P1")
		if err != nil || !ok {
			t.Fatalf("%s: 最近有效结果应仍指向 S1: %+v ok=%v err=%v",
				label, latest, ok, err)
		}
		checkConfirmedResubmit(t, latest, label+"最近有效结果")
		if !reflect.DeepEqual(latest, confirmed) {
			t.Fatalf("%s: 最近有效结果被改动:\n got %+v\nwant %+v",
				label, latest, confirmed)
		}
	}

	// 1) 仅把 COD 改成另一个有限数（30 -> 31）：内容冲突，整体拒绝。
	conflict, err := s.SubmitSample("S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 31})
	if !errors.Is(err, ErrSampleConflict) {
		t.Fatalf("COD 改值重报应报样品内容冲突, got %v", err)
	}
	checkEmptySample(t, conflict, "COD 改值重报")
	assertUntouched("COD 改值被拒绝后")

	// 2) 同一份重报两个去空白后同名的项目，且值也相同：项目重复。
	//    若错误地先合并同名项，内容恰好与原记录相同——绝不能因此幂等返回。
	dup, err := s.SubmitSample(" S1 ", "P1", at(10, 0),
		Measurement{Item: " pH ", Value: 9},
		Measurement{Item: "pH", Value: 9},
		Measurement{Item: " COD ", Value: 30})
	if !errors.Is(err, ErrDuplicateItem) {
		t.Fatalf("同份重报同名项目应报项目重复, got %v", err)
	}
	checkEmptySample(t, dup, "同份同名项目重报")
	assertUntouched("同份同名项目被拒绝后")

	// 3) 两种拒绝之后，提交合法的原内容（换序、空白、另一时区），
	//    仍返回原来的已确认记录，依据与排列都保持首次保存的样子。
	cst := time.FixedZone("CST", 8*3600)
	again, err := s.SubmitSample("  S1  ", " P1 ",
		time.Date(2026, 9, 10, 8, 0, 0, 0, cst),
		Measurement{Item: "COD", Value: 30}, Measurement{Item: " pH ", Value: 9})
	if err != nil {
		t.Fatalf("拒绝后重报合法原内容不应失败: %v", err)
	}
	checkConfirmedResubmit(t, again, "拒绝后合法重报")
	if !reflect.DeepEqual(again, confirmed) {
		t.Fatalf("拒绝后合法重报应返回原已确认记录:\n got %+v\nwant %+v",
			again, confirmed)
	}
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("合法重报不应产生新样品: %+v err=%v", list, err)
	}
}
