package water

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// 本文件的测试只针对一件事：已作废样品被同编号、同内容重复录入（重报）时，
// 只能把原作废记录作为“历史记录”幂等返回，不能借重报恢复它作为有效结果的
// 资格——状态不能变回待判定或已确认，作废前保存的逐项判定依据不能被当作
// 新结论重新参与最近有效结果。先确认后作废与从未确认就作废两种来路都覆盖：
// 前者的测量、逐项依据、整份超标标记与作废原因原样保留；后者本来就没有
// 逐项依据，重报也不能凭空判定，为假的超标标记不能解释成达标。
// 内容冲突沿用现有的 ErrSampleConflict 整体拒绝并返回空样品；拒绝不留痕，
// 之后提交合法原内容仍返回同一份作废记录。录入、查询、确认入口与错误含义
// 均保持现状。

// checkVoidedS1 完整核对 S1 重报返回的就是作废时保存的那条历史记录：
// 状态必须仍是已作废并带原因；pH 9 > 8@09-01 超标、COD 30 == 30@09-01
// 达标、整份超标等作废前保存的内容逐字段保留；Measurements 与 Results
// 的项目排列必须仍是首次录入的 pH、COD 顺序，不能换成重报时的提交顺序。
func checkVoidedS1(t *testing.T, smp Sample, label string) {
	t.Helper()
	if smp.ID != "S1" || smp.PointID != "P1" || smp.Status != StatusVoided {
		t.Fatalf("%s: 重报必须原样返作废记录，不能改变编号、采样点或作废状态: %+v", label, smp)
	}
	if smp.VoidReason != "采样瓶破损" {
		t.Fatalf("%s: 作废原因应原样保留: %+v", label, smp)
	}
	if !smp.SampledAt.Equal(at(10, 0)) {
		t.Fatalf("%s: 采样时间被改动: %+v", label, smp)
	}
	if !smp.Exceeded {
		t.Fatalf("%s: 整份超标标记应保持作废前保存的 true: %+v", label, smp)
	}
	if len(smp.Measurements) != 2 || len(smp.Results) != 2 {
		t.Fatalf("%s: 测量项目或历史判定依据数量被改动: %+v", label, smp)
	}
	wantOrder := []string{"pH", "COD"}
	gotM := make([]string, len(smp.Measurements))
	for i, m := range smp.Measurements {
		gotM[i] = m.Item
	}
	if !reflect.DeepEqual(gotM, wantOrder) {
		t.Fatalf("%s: 测量排列应保持首次录入的 pH、COD，不能换成重报顺序: %v",
			label, gotM)
	}
	gotR := make([]string, len(smp.Results))
	for i, r := range smp.Results {
		gotR[i] = r.Item
	}
	if !reflect.DeepEqual(gotR, wantOrder) {
		t.Fatalf("%s: 逐项结果排列应保持首次录入的 pH、COD: %v", label, gotR)
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
		t.Fatalf("%s: pH 历史依据应为 9 > 8@09-01 超标: %+v", label, ph)
	}
	cod, ok := rs["COD"]
	if !ok || cod.Value != 30 || cod.Limit != 30.0 || !cod.LimitEffective.Equal(at(1, 0)) || cod.Exceeded {
		t.Fatalf("%s: COD 历史依据应为 30 == 30@09-01 达标: %+v", label, cod)
	}
}

// checkEarlierS0 完整核对最近有效结果退回的较早样品 S0：
// 返回内容必须整体属于 S0（pH 7、COD 20，两项与整份均达标），
// 不能混入作废记录 S1 的 pH 9、超标依据或任何字段。
func checkEarlierS0(t *testing.T, smp Sample, label string) {
	t.Helper()
	if smp.ID != "S0" || smp.PointID != "P1" || smp.Status != StatusConfirmed || smp.VoidReason != "" {
		t.Fatalf("%s: 最近有效结果应整体属于较早的 S0: %+v", label, smp)
	}
	if !smp.SampledAt.Equal(at(5, 0)) {
		t.Fatalf("%s: 采样时间不属于 S0: %+v", label, smp)
	}
	if smp.Exceeded || len(smp.Measurements) != 2 || len(smp.Results) != 2 {
		t.Fatalf("%s: S0 应整份达标并带两项测量与判定: %+v", label, smp)
	}
	ms := map[string]float64{}
	for _, m := range smp.Measurements {
		ms[m.Item] = m.Value
	}
	if ms["pH"] != 7 || ms["COD"] != 20 {
		t.Fatalf("%s: 不能混入作废记录 S1 的测量值: %+v", label, smp.Measurements)
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

// 先确认、后作废的样品 S1（pH 9 > 8@09-01 超标，COD 30 == 30@09-01 达标，
// 整份超标），同一点位另有一份较早的已确认样品 S0。用原编号、采样点、
// 采样时间和项目测量值重报 S1，必须成功返回原作废记录：状态、作废原因、
// 测量值、逐项依据、整份超标标记与作废时保存的快照逐字段一致，项目排列
// 保持首次录入的 pH、COD。项目换序、编号/采样点/项目名带首尾空白、
// 换时区表示同一采样瞬间都按现有同内容规则幂等处理。
//
// 重报后该编号在按点列表中仍只出现一次；最近有效结果继续排除作废记录，
// 整体退到较早的 S0 而不混入 S1 的任何测量或依据；对它再请求确认仍是
// ErrVoided 加空样品，历史依据不能被当成新结论。幂等重报不写盘，
// 关闭后重新打开，在新进程里再重报原内容仍返回同一份作废记录。
func TestResubmitVoidedReturnsHistoryNotEligibility(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))

	// 较早的已确认样品 S0：pH 7、COD 20，两项达标。
	mustSample(t, s, "S0", "P1", at(5, 0),
		Measurement{Item: "pH", Value: 7}, Measurement{Item: "COD", Value: 20})
	earlier, err := s.Confirm("S0")
	if err != nil {
		t.Fatalf("Confirm S0: %v", err)
	}
	checkEarlierS0(t, earlier, "较早样品确认")

	// 较晚样品 S1：一项超标、一项恰好等于上限，确认后整份超标，再作废。
	mustSample(t, s, "S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	if _, err := s.Confirm("S1"); err != nil {
		t.Fatalf("Confirm S1: %v", err)
	}
	voided, err := s.Void("S1", " 采样瓶破损 ")
	if err != nil {
		t.Fatalf("Void S1: %v", err)
	}
	checkVoidedS1(t, voided, "首次作废返回")

	cst := time.FixedZone("CST", 8*3600) // 2026-09-10 08:00 +08:00 == 00:00 UTC
	cases := []struct {
		name      string
		id        string
		point     string
		sampledAt time.Time
		ms        []Measurement
	}{
		{
			name:      "原内容原顺序重报",
			id:        "S1",
			point:     "P1",
			sampledAt: at(10, 0),
			ms: []Measurement{
				{Item: "pH", Value: 9},
				{Item: "COD", Value: 30},
			},
		},
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
		checkVoidedS1(t, got, "重报（"+c.name+"）")
		// 重报只能取回作废时的历史快照：不能退回待判定/已确认，
		// 不能重算依据，也不能用后一次提交的排列替换。
		if !reflect.DeepEqual(got, voided) {
			t.Fatalf("重报（%s）应原样返回作废记录:\n got %+v\nwant %+v",
				c.name, got, voided)
		}
	}

	// 按采样点查看：重报不产生新样品，S1 只出现一次且仍是作废快照。
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListByPoint 应只有 S0、S1 两份: %+v err=%v", list, err)
	}
	if list[0].ID != "S1" || list[1].ID != "S0" {
		t.Fatalf("排列应为 S1 在前 S0 在后: %+v", list)
	}
	checkVoidedS1(t, list[0], "重报后按点查询中的 S1")
	if !reflect.DeepEqual(list[0], voided) {
		t.Fatalf("按点查询中的作废记录被改动:\n got %+v\nwant %+v", list[0], voided)
	}
	checkEarlierS0(t, list[1], "重报后按点查询中的 S0")

	// 最近有效结果继续排除作废记录，整体退到较早的 S0，
	// 返回内容不能混入 S1 的测量或依据。
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("最近有效结果应指向 S0: %+v ok=%v err=%v", latest, ok, err)
	}
	checkEarlierS0(t, latest, "重报后最近有效结果")
	if !reflect.DeepEqual(latest, earlier) {
		t.Fatalf("最近有效结果应与 S0 的已确认记录一致:\n got %+v\nwant %+v",
			latest, earlier)
	}

	// 对重报后的作废样品再请求确认：仍是现有作废错误加空样品，
	// 保留下来的历史依据不能被当作新的有效结论。
	reconfirmed, err := s.Confirm(" S1 ")
	if !errors.Is(err, ErrVoided) {
		t.Fatalf("重报后再次确认应返回作废错误, got %v", err)
	}
	checkEmptySample(t, reconfirmed, "重报后再次确认")

	// 被拒绝的确认不留任何变更：列表仍是两份，S1 仍是作废快照，
	// 最近有效结果仍是 S0。
	list, err = s.ListByPoint("P1")
	if err != nil || len(list) != 2 {
		t.Fatalf("拒绝确认后 ListByPoint 应仍只有 S0、S1: %+v err=%v", list, err)
	}
	checkVoidedS1(t, list[0], "拒绝确认后按点查询中的 S1")
	if !reflect.DeepEqual(list[0], voided) {
		t.Fatalf("拒绝确认不得改动作废记录:\n got %+v\nwant %+v", list[0], voided)
	}
	latest, ok, err = s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("拒绝确认后最近有效结果应仍指向 S0: %+v ok=%v err=%v",
			latest, ok, err)
	}
	checkEarlierS0(t, latest, "拒绝确认后最近有效结果")

	// 幂等重报不写盘：关闭后重新打开同一目录，作废状态、原因与历史依据
	// 原样保留，新进程里重报原内容仍返回同一份作废记录。
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
		t.Fatalf("重开后 ListByPoint 应仍只有 S0、S1: %+v err=%v", list2, err)
	}
	checkVoidedS1(t, list2[0], "重开后按点查询中的 S1")
	latest2, ok, err := reopened.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("重开后最近有效结果应仍指向 S0: %+v ok=%v err=%v", latest2, ok, err)
	}
	checkEarlierS0(t, latest2, "重开后最近有效结果")

	again, err := reopened.SubmitSample(" S1 ", " P1 ",
		time.Date(2026, 9, 10, 8, 0, 0, 0, cst),
		Measurement{Item: "COD", Value: 30}, Measurement{Item: " pH ", Value: 9})
	if err != nil {
		t.Fatalf("重开后重报原内容: %v", err)
	}
	checkVoidedS1(t, again, "重开后重报")
	if !reflect.DeepEqual(again, voided) {
		t.Fatalf("重开后重报应返回同一份作废记录:\n got %+v\nwant %+v", again, voided)
	}
	reconfirmed2, err := reopened.Confirm("S1")
	if !errors.Is(err, ErrVoided) {
		t.Fatalf("重开后再次确认仍应返回作废错误, got %v", err)
	}
	checkEmptySample(t, reconfirmed2, "重开后再次确认")
	list3, err := reopened.ListByPoint("P1")
	if err != nil || len(list3) != 2 {
		t.Fatalf("重开后重报不应产生新样品: %+v err=%v", list3, err)
	}
}

// 同一编号重报已作废样品时，只要一个测量值不同（即使其他内容全部一致，
// 无论改动的是超标项还是恰好等于上限的达标项），都必须按现有的
// ErrSampleConflict 整体拒绝并返回空样品。拒绝不留痕：原作废记录的状态、
// 原因、测量、逐项依据与整份超标标记不变，按点列表中该编号仍只有一份，
// 最近有效结果也不改变。拒绝之后再次提交合法的原内容（可带换序、空白、
// 另一时区的等价差别），仍返回同一份作废记录。
func TestResubmitVoidedConflictRejectsAndKeepsRecord(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))
	mustSample(t, s, "S0", "P1", at(5, 0),
		Measurement{Item: "pH", Value: 7}, Measurement{Item: "COD", Value: 20})
	earlier, err := s.Confirm("S0")
	if err != nil {
		t.Fatalf("Confirm S0: %v", err)
	}
	mustSample(t, s, "S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	if _, err := s.Confirm("S1"); err != nil {
		t.Fatalf("Confirm S1: %v", err)
	}
	voided, err := s.Void("S1", "采样瓶破损")
	if err != nil {
		t.Fatalf("Void S1: %v", err)
	}

	// 每次被拒绝后，作废记录与最近有效结果都必须原封不动。
	assertUntouched := func(label string) {
		t.Helper()
		list, err := s.ListByPoint("P1")
		if err != nil || len(list) != 2 {
			t.Fatalf("%s: ListByPoint 应仍只有 S0、S1 两份: %+v err=%v",
				label, list, err)
		}
		checkVoidedS1(t, list[0], label+"按点查询中的 S1")
		if !reflect.DeepEqual(list[0], voided) {
			t.Fatalf("%s: 作废记录被改动:\n got %+v\nwant %+v",
				label, list[0], voided)
		}
		latest, ok, err := s.LatestResult("P1")
		if err != nil || !ok {
			t.Fatalf("%s: 最近有效结果应仍指向 S0: %+v ok=%v err=%v",
				label, latest, ok, err)
		}
		checkEarlierS0(t, latest, label+"最近有效结果")
		if !reflect.DeepEqual(latest, earlier) {
			t.Fatalf("%s: 最近有效结果被改动:\n got %+v\nwant %+v",
				label, latest, earlier)
		}
	}

	// 1) 超标项 pH 改值（9 -> 10），其他内容一致：内容冲突。
	conflictPH, err := s.SubmitSample(" S1 ", "P1", at(10, 0),
		Measurement{Item: " pH ", Value: 10}, Measurement{Item: "COD", Value: 30})
	if !errors.Is(err, ErrSampleConflict) {
		t.Fatalf("pH 改值重报应报样品内容冲突, got %v", err)
	}
	checkEmptySample(t, conflictPH, "pH 改值重报")
	assertUntouched("pH 改值被拒绝后")

	// 2) 恰好等于上限的达标项 COD 改值（30 -> 31），其他内容一致：同样冲突。
	conflictCOD, err := s.SubmitSample("S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: " COD ", Value: 31})
	if !errors.Is(err, ErrSampleConflict) {
		t.Fatalf("COD 改值重报应报样品内容冲突, got %v", err)
	}
	checkEmptySample(t, conflictCOD, "COD 改值重报")
	assertUntouched("COD 改值被拒绝后")

	// 3) 拒绝之后提交合法原内容（换序、空白、另一时区），
	//    仍返回同一份作废记录，资格不恢复，依据与排列保持首次保存的样子。
	cst := time.FixedZone("CST", 8*3600)
	again, err := s.SubmitSample("  S1  ", " P1 ",
		time.Date(2026, 9, 10, 8, 0, 0, 0, cst),
		Measurement{Item: "COD", Value: 30}, Measurement{Item: " pH ", Value: 9})
	if err != nil {
		t.Fatalf("拒绝后重报合法原内容不应失败: %v", err)
	}
	checkVoidedS1(t, again, "拒绝后合法重报")
	if !reflect.DeepEqual(again, voided) {
		t.Fatalf("拒绝后合法重报应返回同一份作废记录:\n got %+v\nwant %+v",
			again, voided)
	}
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 2 {
		t.Fatalf("合法重报不应产生新样品: %+v err=%v", list, err)
	}
}

// 从未确认就作废的样品 S2（pH 9、COD 30，采样时刻两项上限均已适用，
// 但作废前没有保存过任何判定依据），合法重报后仍必须没有逐项判定依据，
// 状态仍是已作废，整份超标标记保持作废前的零值 false——该 false 只是
// 保留下来的原值，既不是达标结论也不能被重报补成结论。该点没有其他
// 有效已确认样品时，最近结果明确返回空样品、ok 为假且不报错；
// 对它再确认仍是 ErrVoided 加空样品。内容冲突拒绝不留痕，
// 之后合法重报仍返回同一份无依据的作废记录。
func TestResubmitPendingVoidedKeepsNoBasisAndLatestEmpty(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	// 两项上限在采样时刻都已适用：若重报错误地触发重新判定，就会凭空
	// 生成超标结论，本测试就是要堵住这条路。
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))

	mustSample(t, s, "S2", "P1", at(12, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	voided, err := s.Void(" S2 ", " 录入信息有误 ")
	if err != nil {
		t.Fatalf("Void S2: %v", err)
	}
	checkPendingVoided := func(smp Sample, label string) {
		t.Helper()
		if smp.ID != "S2" || smp.PointID != "P1" || smp.Status != StatusVoided {
			t.Fatalf("%s: 应返回原作废记录且资格不恢复: %+v", label, smp)
		}
		if smp.VoidReason != "录入信息有误" {
			t.Fatalf("%s: 作废原因应原样保留: %+v", label, smp)
		}
		if !smp.SampledAt.Equal(at(12, 0)) {
			t.Fatalf("%s: 采样时间被改动: %+v", label, smp)
		}
		// 作废前没有判定依据，重报不能凭空补出逐项结论或整份超标标记。
		if len(smp.Results) != 0 || smp.Exceeded {
			t.Fatalf("%s: 从未确认即作废的样品重报后仍应无判定依据、超标标记保持原值: %+v",
				label, smp)
		}
		if len(smp.Measurements) != 2 {
			t.Fatalf("%s: 原测量值应保留: %+v", label, smp)
		}
		got := make([]string, len(smp.Measurements))
		for i, m := range smp.Measurements {
			got[i] = m.Item
		}
		if !reflect.DeepEqual(got, []string{"pH", "COD"}) {
			t.Fatalf("%s: 测量排列应保持首次录入顺序: %v", label, got)
		}
		if smp.Measurements[0].Value != 9 || smp.Measurements[1].Value != 30 {
			t.Fatalf("%s: 原测量值被改动: %+v", label, smp.Measurements)
		}
	}
	checkPendingVoided(voided, "直接作废返回")

	// 该点没有其他有效已确认样品：最近结果明确无结果，空样品、ok 假、无错误。
	// 作废记录里为假的超标标记不能推导出达标。
	none, ok, err := s.LatestResult("P1")
	if err != nil || ok {
		t.Fatalf("应明确无结果: %+v ok=%v err=%v", none, ok, err)
	}
	checkEmptySample(t, none, "无最近有效结果")

	cst := time.FixedZone("CST", 8*3600) // 2026-09-12 08:00 +08:00 == 00:00 UTC
	legit := []struct {
		name      string
		id        string
		point     string
		sampledAt time.Time
		ms        []Measurement
	}{
		{
			name:      "原内容重报",
			id:        "S2",
			point:     "P1",
			sampledAt: at(12, 0),
			ms: []Measurement{
				{Item: "pH", Value: 9},
				{Item: "COD", Value: 30},
			},
		},
		{
			name:      "换序空白另一时区",
			id:        "  S2 ",
			point:     " P1 ",
			sampledAt: time.Date(2026, 9, 12, 8, 0, 0, 0, cst),
			ms: []Measurement{
				{Item: " COD ", Value: 30},
				{Item: " pH ", Value: 9},
			},
		},
	}
	for _, c := range legit {
		got, err := s.SubmitSample(c.id, c.point, c.sampledAt, c.ms...)
		if err != nil {
			t.Fatalf("重报原内容（%s）不应被拒绝: %v", c.name, err)
		}
		checkPendingVoided(got, "重报（"+c.name+"）")
		if !reflect.DeepEqual(got, voided) {
			t.Fatalf("重报（%s）应返回同一份作废记录:\n got %+v\nwant %+v",
				c.name, got, voided)
		}
	}

	// 一个测量值不同即冲突拒绝，返回空样品，拒绝不生成任何判定依据。
	conflict, err := s.SubmitSample("S2", "P1", at(12, 0),
		Measurement{Item: "pH", Value: 9.5}, Measurement{Item: "COD", Value: 30})
	if !errors.Is(err, ErrSampleConflict) {
		t.Fatalf("改值重报应报样品内容冲突, got %v", err)
	}
	checkEmptySample(t, conflict, "改值重报")
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("拒绝重报不应留下新记录: %+v err=%v", list, err)
	}
	checkPendingVoided(list[0], "冲突被拒后按点查询")
	if none, ok, err := s.LatestResult("P1"); err != nil || ok {
		t.Fatalf("冲突被拒后仍应无最近有效结果: %+v ok=%v err=%v", none, ok, err)
	}

	// 再提交合法原内容，仍返回同一份无依据的作废记录。
	again, err := s.SubmitSample("S2", "P1", at(12, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	if err != nil {
		t.Fatalf("拒绝后重报合法原内容不应失败: %v", err)
	}
	checkPendingVoided(again, "拒绝后合法重报")
	if !reflect.DeepEqual(again, voided) {
		t.Fatalf("拒绝后合法重报应返回同一份作废记录:\n got %+v\nwant %+v",
			again, voided)
	}

	// 对重报后的作废样品再确认：仍是作废错误加空样品，不会借确认补出结论。
	reconfirmed, err := s.Confirm("S2")
	if !errors.Is(err, ErrVoided) {
		t.Fatalf("作废样品再次确认应返回作废错误, got %v", err)
	}
	checkEmptySample(t, reconfirmed, "再次确认")

	// 关闭重开后，作废状态、无依据与“最近结果为空”都随落盘保持，
	// 新进程里重报仍返回同一份记录。
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
		t.Fatalf("重开后 ListByPoint 应仍只有 S2: %+v err=%v", list2, err)
	}
	checkPendingVoided(list2[0], "重开后按点查询")
	if none, ok, err := reopened.LatestResult("P1"); err != nil || ok {
		t.Fatalf("重开后仍应明确无结果: %+v ok=%v err=%v", none, ok, err)
	}
	reopenedAgain, err := reopened.SubmitSample(" S2 ", " P1 ",
		time.Date(2026, 9, 12, 8, 0, 0, 0, cst),
		Measurement{Item: "COD", Value: 30}, Measurement{Item: "pH", Value: 9})
	if err != nil {
		t.Fatalf("重开后重报原内容: %v", err)
	}
	checkPendingVoided(reopenedAgain, "重开后重报")
	if !reflect.DeepEqual(reopenedAgain, voided) {
		t.Fatalf("重开后重报应返回同一份作废记录:\n got %+v\nwant %+v",
			reopenedAgain, voided)
	}
}
