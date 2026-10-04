package water

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// 本文件的测试只针对一件事：已作废样品被同编号、同内容重复录入（重报）时，
// 只能返回作废时保存的历史记录——状态保持已作废、作废原因、测量值、逐项
// 判定依据与整份超标标记原样保留，不能借重报恢复成待判定或已确认，也不能
// 因为记录里留着旧判定依据就重新算作有效结果。项目换序、编号与项目名带
// 首尾空白、另一时区表示同一采样瞬间都按现有同内容规则幂等返回，且返回的
// 项目排列保持首次录入的顺序。只要一个测量值不同就按现有的
// ErrSampleConflict 整体拒绝并返回空样品，拒绝不留痕。录入、查询入口与
// 错误含义均保持现状。

// checkVoidedResubmit 锁定先确认、后作废的样品 S1 被同内容重报时返回的就是
// 作废时保存的那条记录：已作废状态、作废原因、采样时间、pH 9 > 8@09-01 超标、
// COD 30 == 30@09-01 达标、整份超标标记全部保持原样；Measurements 与
// Results 的项目排列必须是首次录入的 pH、COD 顺序，不能换成重报顺序。
func checkVoidedResubmit(t *testing.T, smp Sample, label string) {
	t.Helper()
	if smp.ID != "S1" || smp.PointID != "P1" || smp.Status != StatusVoided {
		t.Fatalf("%s: 基本字段被改动（重报不能恢复有效资格）: %+v", label, smp)
	}
	if !smp.SampledAt.Equal(at(10, 0)) || smp.VoidReason != "复测确认样品污染" {
		t.Fatalf("%s: 采样时间或作废原因被改动: %+v", label, smp)
	}
	if !smp.Exceeded {
		t.Fatalf("%s: 整份超标标记应保持作废前保存的 true: %+v", label, smp)
	}
	if len(smp.Measurements) != 2 || len(smp.Results) != 2 {
		t.Fatalf("%s: 测量项目或判定结果数量被改动: %+v", label, smp)
	}
	want := []string{"pH", "COD"}
	gotM := make([]string, len(smp.Measurements))
	for i, m := range smp.Measurements {
		gotM[i] = m.Item
	}
	if !reflect.DeepEqual(gotM, want) {
		t.Fatalf("%s: 测量排列应保持首次录入的 pH、COD，不能用重报顺序替换: %v",
			label, gotM)
	}
	gotR := make([]string, len(smp.Results))
	for i, r := range smp.Results {
		gotR[i] = r.Item
	}
	if !reflect.DeepEqual(gotR, want) {
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
		t.Fatalf("%s: pH 应保留 9 > 8@09-01 超标的历史依据: %+v", label, ph)
	}
	cod, ok := rs["COD"]
	if !ok || cod.Value != 30 || cod.Limit != 30.0 || !cod.LimitEffective.Equal(at(1, 0)) || cod.Exceeded {
		t.Fatalf("%s: COD 应保留 30 == 上限 30@09-01 达标的历史依据: %+v", label, cod)
	}
}

// checkEarlierConfirmed 锁定较早的已确认达标样品 S0 的完整内容：
// pH 7、COD 20 均不超标，整份达标。最近有效结果退到它时，返回内容必须
// 整体属于 S0，不能混入作废样品 S1 的 pH 9 等任何测量或依据。
func checkEarlierConfirmed(t *testing.T, smp Sample, label string) {
	t.Helper()
	if smp.ID != "S0" || smp.PointID != "P1" || smp.Status != StatusConfirmed {
		t.Fatalf("%s: 基本字段被改动: %+v", label, smp)
	}
	if !smp.SampledAt.Equal(at(5, 0)) || smp.VoidReason != "" {
		t.Fatalf("%s: 采样时间或作废原因被改动: %+v", label, smp)
	}
	if smp.Exceeded {
		t.Fatalf("%s: 较早样品两项均达标，整份不应超标: %+v", label, smp)
	}
	if len(smp.Measurements) != 2 || len(smp.Results) != 2 {
		t.Fatalf("%s: 测量项目或判定结果数量被改动: %+v", label, smp)
	}
	ms := map[string]float64{}
	for _, m := range smp.Measurements {
		ms[m.Item] = m.Value
	}
	if ms["pH"] != 7 || ms["COD"] != 20 {
		t.Fatalf("%s: 测量值必须整体属于较早样品，不能混入作废记录的数值: %+v",
			label, smp.Measurements)
	}
	rs := map[string]ItemResult{}
	for _, r := range smp.Results {
		rs[r.Item] = r
	}
	ph, ok := rs["pH"]
	if !ok || ph.Value != 7 || ph.Limit != 8.0 || !ph.LimitEffective.Equal(at(1, 0)) || ph.Exceeded {
		t.Fatalf("%s: pH 应为 7 <= 8@09-01 达标，不能混入作废记录的依据: %+v", label, ph)
	}
	cod, ok := rs["COD"]
	if !ok || cod.Value != 20 || cod.Limit != 30.0 || !cod.LimitEffective.Equal(at(1, 0)) || cod.Exceeded {
		t.Fatalf("%s: COD 应为 20 <= 30@09-01 达标，不能混入作废记录的依据: %+v", label, cod)
	}
}

// setupVoidedTwoItem 准备同一采样点上的两份样品：较早的 S0（09-05 采样，
// pH 7、COD 20，已确认达标）和较晚的 S1（09-10 采样，pH 9 超标、
// COD 30 恰好等于上限达标，整份超标，先确认后作废）。返回作废时保存的记录。
func setupVoidedTwoItem(t *testing.T, s *Store) Sample {
	t.Helper()
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))
	mustSample(t, s, "S0", "P1", at(5, 0),
		Measurement{Item: "pH", Value: 7}, Measurement{Item: "COD", Value: 20})
	if _, err := s.Confirm("S0"); err != nil {
		t.Fatalf("Confirm S0: %v", err)
	}
	mustSample(t, s, "S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	if _, err := s.Confirm("S1"); err != nil {
		t.Fatalf("Confirm S1: %v", err)
	}
	voided, err := s.Void("S1", "  复测确认样品污染  ")
	if err != nil {
		t.Fatalf("Void S1: %v", err)
	}
	checkVoidedResubmit(t, voided, "作废返回值")
	return voided
}

// 先确认、后作废的样品 S1 用原编号、原采样点、原采样时间和原项目测量值重报：
// 应成功返回原作废记录，状态、作废原因、测量值、逐项依据和整份超标标记都
// 保持原样——重报只是幂等返回历史记录，不能把作废记录变回待判定或已确认。
// 项目换序、编号及项目名带首尾空白、另一时区表示同一采样瞬间，都按现有
// 同内容规则处理；返回的项目排列保持首次录入的 pH、COD 顺序。
// 重报后该编号在按点列表中仍只有一份记录；最近有效结果继续排除它，退到
// 较早的已确认样品 S0 且内容整体属于 S0；对重报后的作废样品再请求确认，
// 仍返回 ErrVoided 和空样品，历史依据不能被当作新结论。
func TestResubmitVoidedReturnsHistoricalRecord(t *testing.T) {
	s, _ := open(t)
	voided := setupVoidedTwoItem(t, s)

	cst := time.FixedZone("CST", 8*3600) // 2026-09-10 08:00 +08:00 == 00:00 UTC
	cases := []struct {
		name      string
		id        string
		point     string
		sampledAt time.Time
		ms        []Measurement
	}{
		{
			name:      "原样重报",
			id:        "S1",
			point:     "P1",
			sampledAt: at(10, 0),
			ms: []Measurement{
				{Item: "pH", Value: 9},
				{Item: "COD", Value: 30},
			},
		},
		{
			name:      "项目换序",
			id:        "S1",
			point:     "P1",
			sampledAt: at(10, 0),
			ms: []Measurement{
				{Item: "COD", Value: 30},
				{Item: "pH", Value: 9},
			},
		},
		{
			name:      "编号及项目名带首尾空白",
			id:        "  S1  ",
			point:     " P1 ",
			sampledAt: at(10, 0),
			ms: []Measurement{
				{Item: "  pH ", Value: 9},
				{Item: " COD  ", Value: 30},
			},
		},
		{
			name:      "另一时区表示同一采样瞬间",
			id:        "S1",
			point:     "P1",
			sampledAt: time.Date(2026, 9, 10, 8, 0, 0, 0, cst),
			ms: []Measurement{
				{Item: "pH", Value: 9},
				{Item: "COD", Value: 30},
			},
		},
		{
			name:      "换序空白时区差别叠加",
			id:        " S1 ",
			point:     "P1",
			sampledAt: time.Date(2026, 9, 10, 8, 0, 0, 0, cst),
			ms: []Measurement{
				{Item: " COD ", Value: 30},
				{Item: "pH", Value: 9},
			},
		},
	}
	for _, c := range cases {
		got, err := s.SubmitSample(c.id, c.point, c.sampledAt, c.ms...)
		if err != nil {
			t.Fatalf("作废样品同内容重报（%s）不应被拒绝: %v", c.name, err)
		}
		checkVoidedResubmit(t, got, "重报（"+c.name+"）")
		// 重报返回的就是作废时保存的那条记录，不是重新录入或重新判定的结果。
		if !reflect.DeepEqual(got, voided) {
			t.Fatalf("重报（%s）应原样返回作废记录:\n got %+v\nwant %+v",
				c.name, got, voided)
		}
	}

	// 重报后该编号在按点列表中仍只有一份记录，排列仍是 S1 在前、S0 在后。
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 2 {
		t.Fatalf("重报后 ListByPoint 应仍只有 S1、S0 两份: %+v err=%v", list, err)
	}
	if list[0].ID != "S1" || list[1].ID != "S0" {
		t.Fatalf("重报后排列应为 S1 在前 S0 在后: %+v", list)
	}
	checkVoidedResubmit(t, list[0], "重报后按点查询的作废记录")
	checkEarlierConfirmed(t, list[1], "重报后按点查询的较早样品")

	// 最近有效结果继续排除作废样品，退到较早的已确认样品 S0；
	// 返回内容整体属于 S0，不能混入作废记录的测量或依据。
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("重报后最近有效结果应仍指向 S0: %+v ok=%v err=%v", latest, ok, err)
	}
	checkEarlierConfirmed(t, latest, "重报后最近有效结果")

	// 对重报后的作废样品再请求确认：仍返回现有的作废错误和空样品，
	// 保留下来的历史依据不能被当作新结论。
	reconfirmed, err := s.Confirm("S1")
	if !errors.Is(err, ErrVoided) {
		t.Fatalf("对重报后的作废样品再确认应报已作废错误, got %v", err)
	}
	checkEmptySample(t, reconfirmed, "重报后再确认作废样品")

	// 确认被拒绝不留痕：作废记录与最近有效结果都不变。
	list2, err := s.ListByPoint("P1")
	if err != nil || len(list2) != 2 {
		t.Fatalf("再确认被拒绝后 ListByPoint 应不变: %+v err=%v", list2, err)
	}
	checkVoidedResubmit(t, list2[0], "再确认被拒绝后按点查询的作废记录")
	latest2, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("再确认被拒绝后最近有效结果应仍指向 S0: %+v ok=%v err=%v", latest2, ok, err)
	}
	checkEarlierConfirmed(t, latest2, "再确认被拒绝后最近有效结果")
}

// 同一编号重报时只要一个测量值不同（COD 30 改成 31），即使其他内容一致，
// 也要按现有的样品内容冲突整体拒绝并返回空样品；原作废记录及最近有效结果
// 均不改变。拒绝之后再次提交原内容，仍返回同一份作废记录。
func TestResubmitVoidedConflictKeepsRecord(t *testing.T) {
	s, _ := open(t)
	voided := setupVoidedTwoItem(t, s)

	conflict, err := s.SubmitSample("S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 31})
	if !errors.Is(err, ErrSampleConflict) {
		t.Fatalf("改值重报作废样品应报样品内容冲突, got %v", err)
	}
	checkEmptySample(t, conflict, "改值重报作废样品")

	// 拒绝不留痕：原作废记录保持原样，该编号仍只有一份记录。
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 2 {
		t.Fatalf("冲突拒绝后 ListByPoint 应不变: %+v err=%v", list, err)
	}
	if list[0].ID != "S1" || list[1].ID != "S0" {
		t.Fatalf("冲突拒绝后排列应为 S1 在前 S0 在后: %+v", list)
	}
	checkVoidedResubmit(t, list[0], "冲突拒绝后按点查询的作废记录")

	// 最近有效结果仍排除作废样品，整体属于较早的 S0。
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("冲突拒绝后最近有效结果应仍指向 S0: %+v ok=%v err=%v", latest, ok, err)
	}
	checkEarlierConfirmed(t, latest, "冲突拒绝后最近有效结果")

	// 拒绝后再次提交原内容（带换序、空白等价差别），仍返回同一份作废记录。
	again, err := s.SubmitSample(" S1 ", "P1", at(10, 0),
		Measurement{Item: " COD ", Value: 30}, Measurement{Item: "pH", Value: 9})
	if err != nil {
		t.Fatalf("冲突拒绝后重报合法原内容不应失败: %v", err)
	}
	checkVoidedResubmit(t, again, "冲突拒绝后合法重报")
	if !reflect.DeepEqual(again, voided) {
		t.Fatalf("冲突拒绝后合法重报应返回同一份作废记录:\n got %+v\nwant %+v",
			again, voided)
	}
}

// 从未确认就作废的样品在合法重报后仍没有逐项判定依据，超标标记保持原值
// false——重报只是返回历史记录，作废不会凭空生成结论。该点没有其他有效
// 已确认样品时，最近结果明确返回空样品、ok == false 且不报错，不能由
// 为假的超标标记推导出达标。
func TestResubmitVoidedPendingKeepsNoConclusion(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P2", "排水口")
	mustLimit(t, s, "P2", "pH", 8.0, at(1, 0))
	mustSample(t, s, "SX", "P2", at(12, 0), Measurement{Item: "pH", Value: 7})
	voided, err := s.Void("SX", "录入信息有误")
	if err != nil {
		t.Fatalf("Void SX: %v", err)
	}
	checkVoidedPending := func(smp Sample, label string) {
		t.Helper()
		if smp.ID != "SX" || smp.PointID != "P2" || smp.Status != StatusVoided {
			t.Fatalf("%s: 基本字段被改动: %+v", label, smp)
		}
		if !smp.SampledAt.Equal(at(12, 0)) || smp.VoidReason != "录入信息有误" {
			t.Fatalf("%s: 采样时间或作废原因被改动: %+v", label, smp)
		}
		if len(smp.Measurements) != 1 ||
			smp.Measurements[0] != (Measurement{Item: "pH", Value: 7}) {
			t.Fatalf("%s: 原测量值被改动: %+v", label, smp.Measurements)
		}
		// 从未确认就作废：重报后仍没有逐项判定依据，超标标记保持为假。
		if len(smp.Results) != 0 || smp.Exceeded {
			t.Fatalf("%s: 重报不能凭空生成判定依据或结论: %+v", label, smp)
		}
	}
	checkVoidedPending(voided, "作废返回值")

	// 合法重报（编号、采样点、项目名带首尾空白，另一时区表示同一采样瞬间）
	// 仍返回同一份作废记录，不补出逐项判定，也不改变超标标记。
	cst := time.FixedZone("CST", 8*3600) // 2026-09-12 08:00 +08:00 == 00:00 UTC
	got, err := s.SubmitSample(" SX  ", " P2 ",
		time.Date(2026, 9, 12, 8, 0, 0, 0, cst),
		Measurement{Item: "  pH ", Value: 7})
	if err != nil {
		t.Fatalf("未确认即作废的样品同内容重报不应被拒绝: %v", err)
	}
	checkVoidedPending(got, "合法重报")
	if !reflect.DeepEqual(got, voided) {
		t.Fatalf("合法重报应原样返回作废记录:\n got %+v\nwant %+v", got, voided)
	}

	// 按点列表仍只有这一份作废记录。
	list, err := s.ListByPoint("P2")
	if err != nil || len(list) != 1 {
		t.Fatalf("重报后 ListByPoint 应仍只有 SX 一份: %+v err=%v", list, err)
	}
	checkVoidedPending(list[0], "重报后按点查询")

	// 该点没有其他有效已确认样品：最近结果明确返回空样品、无结果且不报错。
	// 这既不是达标也不是超标，不能由为假的超标标记推导出达标。
	latest, ok, err := s.LatestResult("P2")
	if err != nil {
		t.Fatalf("无有效样品时最近结果不应报错: %v", err)
	}
	if ok {
		t.Fatalf("无有效样品时最近结果应明确无结果: %+v", latest)
	}
	checkEmptySample(t, latest, "无有效样品时最近结果")

	// 对重报后的作废样品再请求确认，仍返回作废错误和空样品。
	reconfirmed, err := s.Confirm("SX")
	if !errors.Is(err, ErrVoided) {
		t.Fatalf("对重报后的作废样品再确认应报已作废错误, got %v", err)
	}
	checkEmptySample(t, reconfirmed, "重报后再确认作废样品")
}
