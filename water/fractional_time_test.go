package water

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件围绕“按采样时刻选用限值”的小数秒时间边界补回归保障：
// 接受带小数秒的时间，确认时只取采样当时（精确到纳秒的真实时刻）已经生效的
// 最近一版限值；同一秒内不同纳秒的采样时刻必须区分，限值生效边界前一纳秒用
// 旧版、恰好生效与后一纳秒用新版。录入、确认、查询、作废的既有业务行为不变。

const (
	turbItem = "浊度"
	flatItem = "pH" // 三份样品里上限始终未变更的对照项目
)

// turbBoundary 是浊度限值切换的真实时刻：2026-09-10 12:00:00.5 UTC，
// 自该时刻的第 500000000 纳秒起上限由 10 改为 5。
func turbBoundary() time.Time {
	return time.Date(2026, 9, 10, 12, 0, 0, 500_000_000, time.UTC)
}

// checkBoundarySample 完整核对一份双项目样品在纳秒边界下应保存的内容：
// 浊度采用 wantTurbLimit（生效时间 wantTurbEff，测量值 7 的超标结论随之而定），
// pH 始终使用 6@oldEff 且测量值 6 恰好等于上限、判达标；整份结论只由浊度决定。
// 采样时刻必须连小数秒一起保留，逐项依据必须带实际上限数值与准确生效时刻。
func checkBoundarySample(t *testing.T, smp Sample, wantID string, sampled time.Time,
	wantTurbLimit float64, wantTurbEff, oldEff time.Time, label string,
) {
	t.Helper()
	if smp.ID != wantID || smp.PointID != "P1" || smp.Status != StatusConfirmed || smp.VoidReason != "" {
		t.Fatalf("%s: 编号、采样点、状态或作废原因错误: %+v", label, smp)
	}
	// 采样时间不能丢失小数秒：同一真实时刻且纳秒部分也必须一致。
	if !smp.SampledAt.Equal(sampled) {
		t.Fatalf("%s: 采样真实时刻应为 %s，实际 %s", label, sampled, smp.SampledAt)
	}
	if smp.SampledAt.Nanosecond() != sampled.Nanosecond() {
		t.Fatalf("%s: 采样时间丢失小数秒，纳秒部分应 %d 实际 %d",
			label, sampled.Nanosecond(), smp.SampledAt.Nanosecond())
	}
	if len(smp.Measurements) != 2 || len(smp.Results) != 2 {
		t.Fatalf("%s: 测量或逐项判定数量错误: %+v", label, smp)
	}
	ms := map[string]float64{}
	for _, m := range smp.Measurements {
		ms[m.Item] = m.Value
	}
	if ms[turbItem] != 7 || ms[flatItem] != 6 {
		t.Fatalf("%s: 原始测量值被改动: %+v", label, smp.Measurements)
	}
	rs := map[string]ItemResult{}
	for _, r := range smp.Results {
		rs[r.Item] = r
	}
	turb, ok := rs[turbItem]
	if !ok {
		t.Fatalf("%s: 缺少浊度的逐项判定: %+v", label, smp.Results)
	}
	wantTurbExceeded := 7 > wantTurbLimit
	if turb.Value != 7 || turb.Limit != wantTurbLimit || !turb.LimitEffective.Equal(wantTurbEff) ||
		turb.Exceeded != wantTurbExceeded {
		t.Fatalf("%s: 浊度应采用上限 %g（%s 生效），测量值 7 超标=%v，实际 %+v",
			label, wantTurbLimit, wantTurbEff, wantTurbExceeded, turb)
	}
	// 对照项目上限未变更：三份样品都用同一版限值、同一生效时刻，等于上限判达标。
	flat, ok := rs[flatItem]
	if !ok {
		t.Fatalf("%s: 缺少 %s 的逐项判定: %+v", label, flatItem, smp.Results)
	}
	if flat.Value != 6 || flat.Limit != 6 || !flat.LimitEffective.Equal(oldEff) || flat.Exceeded {
		t.Fatalf("%s: 未变更项目应统一使用 6@%s 且等于上限达标，实际 %+v",
			label, oldEff, flat)
	}
	// 另一项达标不能冲掉浊度的超标结论：任一项目超标则整份超标。
	if smp.Exceeded != wantTurbExceeded {
		t.Fatalf("%s: 整份结论应为 超标=%v，实际 %v", label, wantTurbExceeded, smp.Exceeded)
	}
}

// 同一秒内的三个采样时刻（边界前一纳秒、恰好、后一纳秒）必须各用各的限值：
// 前一纳秒仍是旧上限 10（7 ≤ 10 达标），恰好生效与后一纳秒用新上限 5（7 > 5 超标）。
// 每份逐项依据保留实际采用的上限数值及准确生效时刻，采样时间保留小数秒；
// Confirm 的返回与 ListByPoint 查到的同一记录一致，落盘重开后小数秒不丢。
func TestConfirmNanosecondBoundaryWithinSameSecond(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	oldEff := at(1, 0) // 2026-09-01 UTC，浊度旧版与 pH 唯一一版的生效时间
	boundary := turbBoundary()
	mustLimit(t, s, "P1", turbItem, 10, oldEff)
	mustLimit(t, s, "P1", turbItem, 5, boundary)
	mustLimit(t, s, "P1", flatItem, 6, oldEff)

	before := boundary.Add(-1) // 12:00:00.499999999
	atTime := boundary         // 12:00:00.500000000
	after := boundary.Add(1)   // 12:00:00.500000001
	// 三份样品处在同一秒（Unix 秒数相同），仅纳秒部分不同。
	// 不能只因为年月日时分秒相同就采用同一版限值。
	if before.Unix() != atTime.Unix() || atTime.Unix() != after.Unix() {
		t.Fatal("测试前提：三份采样时间应落在同一秒内")
	}
	if before.Nanosecond() == atTime.Nanosecond() || atTime.Nanosecond() == after.Nanosecond() {
		t.Fatal("测试前提：三份采样时间的纳秒部分必须不同")
	}

	measure := func() []Measurement {
		return []Measurement{{Item: turbItem, Value: 7}, {Item: flatItem, Value: 6}}
	}
	mustSample(t, s, "S-BEFORE", "P1", before, measure()...)
	mustSample(t, s, "S-AT", "P1", atTime, measure()...)
	mustSample(t, s, "S-AFTER", "P1", after, measure()...)

	confirmed := map[string]Sample{}
	for _, id := range []string{"S-BEFORE", "S-AT", "S-AFTER"} {
		smp, err := s.Confirm(id)
		if err != nil {
			t.Fatalf("Confirm %s: %v", id, err)
		}
		confirmed[id] = smp
	}
	// 边界前一纳秒用旧版 10 达标；恰好生效与后一纳秒用新版 5 超标。
	checkBoundarySample(t, confirmed["S-BEFORE"], "S-BEFORE", before, 10, oldEff, oldEff, "确认返回-前一纳秒")
	checkBoundarySample(t, confirmed["S-AT"], "S-AT", atTime, 5, boundary, oldEff, "确认返回-恰好生效")
	checkBoundarySample(t, confirmed["S-AFTER"], "S-AFTER", after, 5, boundary, oldEff, "确认返回-后一纳秒")

	// 按采样点查询：采样时间从晚到早，差一纳秒也要排出先后，三份各自的采样时刻与测量值不混。
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 3 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	wantOrder := []string{"S-AFTER", "S-AT", "S-BEFORE"}
	for i, id := range wantOrder {
		if list[i].ID != id {
			t.Fatalf("排列应为 %v（晚到早，纳秒不同即不同时刻），实际 %+v", wantOrder, list)
		}
	}
	// 确认返回的逐项依据和整份结论必须与按点查询到的相应记录一致。
	listed := map[string]Sample{}
	for _, smp := range list {
		listed[smp.ID] = smp
	}
	checkBoundarySample(t, listed["S-BEFORE"], "S-BEFORE", before, 10, oldEff, oldEff, "按点查询-前一纳秒")
	checkBoundarySample(t, listed["S-AT"], "S-AT", atTime, 5, boundary, oldEff, "按点查询-恰好生效")
	checkBoundarySample(t, listed["S-AFTER"], "S-AFTER", after, 5, boundary, oldEff, "按点查询-后一纳秒")
	// 最近有效结果指向采样最晚（后一纳秒）的那份超标样品。
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("最近有效结果应为 S-AFTER: %+v ok=%v err=%v", latest, ok, err)
	}
	checkBoundarySample(t, latest, "S-AFTER", after, 5, boundary, oldEff, "最近有效结果")

	// 小数秒必须实际落盘：数据文件中应能看到三个带不同小数位的时间串，
	// 限值生效时刻 0.5 与三份采样时刻各自的纳秒都在。
	data, err := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	for _, want := range []string{
		"2026-09-10T12:00:00.499999999Z",
		"2026-09-10T12:00:00.5Z",
		"2026-09-10T12:00:00.500000001Z",
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("落盘数据应保留小数秒时间 %s，实际内容：%s", want, string(data))
		}
	}

	// 关闭后重新打开：小数秒采样时刻、逐项采用的上限与准确生效时间、整份结论原样保留。
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	list2, err := s2.ListByPoint("P1")
	if err != nil || len(list2) != 3 {
		t.Fatalf("重开后 ListByPoint: %+v err=%v", list2, err)
	}
	for _, smp := range list2 {
		switch smp.ID {
		case "S-BEFORE":
			checkBoundarySample(t, smp, "S-BEFORE", before, 10, oldEff, oldEff, "重开-前一纳秒")
		case "S-AT":
			checkBoundarySample(t, smp, "S-AT", atTime, 5, boundary, oldEff, "重开-恰好生效")
		case "S-AFTER":
			checkBoundarySample(t, smp, "S-AFTER", after, 5, boundary, oldEff, "重开-后一纳秒")
		default:
			t.Fatalf("重开后出现意外记录: %+v", smp)
		}
	}
	// 重复确认仍返回已保存的逐项依据，不随后续任何限值变化重算。
	again, err := s2.Confirm("S-AT")
	if err != nil {
		t.Fatalf("重开后重复确认: %v", err)
	}
	checkBoundarySample(t, again, "S-AT", atTime, 5, boundary, oldEff, "重开后重复确认")
}

// 先登记新版本（5@boundary）、之后再乱序补录旧版本（10@更早）时，确认结果仍只由
// 各版生效时刻与采样时刻的关系决定，与登记先后无关：旧版补录前，边界前一纳秒的
// 样品没有任何已生效上限，必须整次拒绝并保持待判定；补录后再确认才用旧版判达标。
// 已用新版确认的恰好/后一纳秒样品，结论与所采用限值不随旧版补录改变。
func TestConfirmNanosecondBoundaryBackfillOrder(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "取水口")
	oldEff := at(1, 0)
	boundary := turbBoundary()
	mustLimit(t, s, "P1", flatItem, 6, oldEff)
	// 先登记新版本，旧版本此刻尚不存在。
	mustLimit(t, s, "P1", turbItem, 5, boundary)

	before := boundary.Add(-1)
	atTime := boundary
	after := boundary.Add(1)
	measure := func() []Measurement {
		return []Measurement{{Item: turbItem, Value: 7}, {Item: flatItem, Value: 6}}
	}
	mustSample(t, s, "S-BEFORE", "P1", before, measure()...)
	mustSample(t, s, "S-AT", "P1", atTime, measure()...)
	mustSample(t, s, "S-AFTER", "P1", after, measure()...)

	// 旧版未补录：前一纳秒时新版 5 尚未生效，没有任何适用上限，整次拒绝。
	got, err := s.Confirm("S-BEFORE")
	if !errors.Is(err, ErrMissingLimit) {
		t.Fatalf("旧版补录前边界前一纳秒应缺适用上限，got %+v err=%v", got, err)
	}
	if got.ID != "" || got.Status != "" {
		t.Fatalf("缺适用上限被拒时应返回空样品，got %+v", got)
	}
	list, _ := s.ListByPoint("P1")
	var pending Sample
	for _, smp := range list {
		if smp.ID == "S-BEFORE" {
			pending = smp
		}
	}
	if pending.ID != "S-BEFORE" || pending.Status != StatusPending || len(pending.Results) != 0 || pending.Exceeded {
		t.Fatalf("被拒样品应保持待判定、无逐项结果与整份结论: %+v", pending)
	}
	if !pending.SampledAt.Equal(before) || pending.SampledAt.Nanosecond() != before.Nanosecond() {
		t.Fatalf("待判定记录的小数秒采样时间丢失: %+v", pending)
	}

	// 恰好生效与后一纳秒：新版已生效，正常判为超标并保存。
	confAt, err := s.Confirm("S-AT")
	if err != nil {
		t.Fatalf("Confirm S-AT: %v", err)
	}
	checkBoundarySample(t, confAt, "S-AT", atTime, 5, boundary, oldEff, "补录前-恰好生效")
	confAfter, err := s.Confirm("S-AFTER")
	if err != nil {
		t.Fatalf("Confirm S-AFTER: %v", err)
	}
	checkBoundarySample(t, confAfter, "S-AFTER", after, 5, boundary, oldEff, "补录前-后一纳秒")

	// 乱序补录旧版（生效时间远早于 boundary）。
	mustLimit(t, s, "P1", turbItem, 10, oldEff)

	// 前一纳秒样品现在应取旧版 10，测量值 7 达标，整份达标。
	confBefore, err := s.Confirm("S-BEFORE")
	if err != nil {
		t.Fatalf("补录旧版后 Confirm S-BEFORE: %v", err)
	}
	checkBoundarySample(t, confBefore, "S-BEFORE", before, 10, oldEff, oldEff, "补录后-前一纳秒")

	// 已确认的两份不重算：仍是新版 5、超标，逐项依据与整份结论保持原样。
	againAt, err := s.Confirm("S-AT")
	if err != nil {
		t.Fatalf("重复确认 S-AT: %v", err)
	}
	checkBoundarySample(t, againAt, "S-AT", atTime, 5, boundary, oldEff, "补录后重复确认-恰好生效")
	againAfter, err := s.Confirm("S-AFTER")
	if err != nil {
		t.Fatalf("重复确认 S-AFTER: %v", err)
	}
	checkBoundarySample(t, againAfter, "S-AFTER", after, 5, boundary, oldEff, "补录后重复确认-后一纳秒")

	// 按点查询与各次确认返回一致，顺序按纳秒时刻晚到早。
	list2, err := s.ListByPoint("P1")
	if err != nil || len(list2) != 3 {
		t.Fatalf("ListByPoint: %+v err=%v", list2, err)
	}
	listed := map[string]Sample{}
	for _, smp := range list2 {
		listed[smp.ID] = smp
	}
	checkBoundarySample(t, listed["S-BEFORE"], "S-BEFORE", before, 10, oldEff, oldEff, "按点查询-前一纳秒")
	checkBoundarySample(t, listed["S-AT"], "S-AT", atTime, 5, boundary, oldEff, "按点查询-恰好生效")
	checkBoundarySample(t, listed["S-AFTER"], "S-AFTER", after, 5, boundary, oldEff, "按点查询-后一纳秒")
	if list2[0].ID != "S-AFTER" || list2[1].ID != "S-AT" || list2[2].ID != "S-BEFORE" {
		t.Fatalf("排列应按纳秒时刻晚到早: %+v", list2)
	}
}

// 采样时刻没有任何已生效版本的边界：某项目唯一一版上限比采样时间晚一纳秒才生效，
// 即使另一项目能判定，整次确认也必须因缺少适用上限而拒绝：样品保持待判定，
// 没有逐项结果或整份结论；既不能借用这个未来版本，也不能把缺失上限当成零。
// 重开数据存放后拒绝结论不变；补录一版恰好于采样时刻生效的上限后，对原样品
// 再确认才成功，且采用的正是恰好生效这版，晚一纳秒的未来版本仍不被借用。
func TestConfirmMissingLimitOneNanosecondBeforeEffective(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	phEff := at(1, 0)
	mustLimit(t, s, "P1", "pH", 8, phEff)

	sampled := turbBoundary() // 12:00:00.5 UTC
	// 未来版本：比采样时间晚一纳秒生效，故意用北京时间表示同一真实时刻，
	// 防止实现按“年月日时分秒”的字面量跨时区比较。
	beijing := time.FixedZone("CST", 8*3600)
	future := time.Date(2026, 9, 10, 20, 0, 0, 500_000_001, beijing)
	if !future.Equal(sampled.Add(1)) {
		t.Fatalf("测试前提：未来版本应等于采样时刻后一纳秒，future=%s sampled=%s", future, sampled)
	}
	mustLimit(t, s, "P1", "COD", 30, future)

	// S1：pH 能判定（7 ≤ 8），COD 只有一版晚一纳秒才生效的上限。
	mustSample(t, s, "S1", "P1", sampled,
		Measurement{Item: "pH", Value: 7}, Measurement{Item: "COD", Value: 3})
	// S2：COD 测量值取负数。若错误地把缺失上限当成零，-1 > 0 为假会“达标”；
	// 正确行为是缺适用上限照样整次拒绝。
	mustSample(t, s, "S2", "P1", sampled.Add(0),
		Measurement{Item: "pH", Value: 7}, Measurement{Item: "COD", Value: -1})

	for _, id := range []string{"S1", "S2"} {
		got, err := s.Confirm(id)
		if !errors.Is(err, ErrMissingLimit) {
			t.Fatalf("%s: 只有晚一纳秒才生效的版本时应缺适用上限拒绝，got %+v err=%v", id, got, err)
		}
		if got.ID != "" || got.Status != "" || got.Exceeded || len(got.Results) != 0 {
			t.Fatalf("%s: 被拒确认应返回空样品、不带任何结论，got %+v", id, got)
		}
	}
	// 原样品继续待判定：没有逐项结果或整份达标结论，小数秒采样时间保留。
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	for _, smp := range list {
		if smp.Status != StatusPending || len(smp.Results) != 0 || smp.Exceeded {
			t.Fatalf("%s: 应保持待判定且无逐项结果/整份结论: %+v", smp.ID, smp)
		}
		if !smp.SampledAt.Equal(sampled) || smp.SampledAt.Nanosecond() != sampled.Nanosecond() {
			t.Fatalf("%s: 小数秒采样时间丢失: %s", smp.ID, smp.SampledAt)
		}
	}
	if latest, ok, err := s.LatestResult("P1"); err != nil || ok {
		t.Fatalf("缺适用上限被拒后不应有最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}

	// 重开数据存放：待判定状态与小数秒采样时间保持，未来版本仍不能借用。
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	for _, id := range []string{"S1", "S2"} {
		if _, err := s2.Confirm(id); !errors.Is(err, ErrMissingLimit) {
			t.Fatalf("重开后 %s 仍应缺适用上限拒绝，got %v", id, err)
		}
	}

	// 补录一版“恰好”于采样时刻生效的上限（30 那版仍晚一纳秒，不能被借用）。
	exactEff := sampled
	mustLimit(t, s2, "P1", "COD", 4, exactEff)
	want := map[string]float64{"S1": 3, "S2": -1}
	for id, value := range want {
		smp, err := s2.Confirm(id)
		if err != nil {
			t.Fatalf("补录恰好生效版本后 Confirm %s: %v", id, err)
		}
		if smp.Status != StatusConfirmed || smp.Exceeded {
			t.Fatalf("%s: 两项均应达标、整份达标: %+v", id, smp)
		}
		if !smp.SampledAt.Equal(sampled) || smp.SampledAt.Nanosecond() != sampled.Nanosecond() {
			t.Fatalf("%s: 小数秒采样时间丢失: %s", id, smp.SampledAt)
		}
		rs := map[string]ItemResult{}
		for _, r := range smp.Results {
			rs[r.Item] = r
		}
		cod, ph := rs["COD"], rs["pH"]
		// 必须采用恰好生效的 4，而不是晚一纳秒的 30；测量值不大于 4 判达标。
		if cod.Value != value || cod.Limit != 4 || !cod.LimitEffective.Equal(exactEff) || cod.Exceeded {
			t.Fatalf("%s: COD 应采用恰好生效的 4@%s 且达标，实际 %+v", id, exactEff, cod)
		}
		// 另一项照常判定，依据不受影响。
		if ph.Value != 7 || ph.Limit != 8 || !ph.LimitEffective.Equal(phEff) || ph.Exceeded {
			t.Fatalf("%s: pH 应采用 8@%s 且达标，实际 %+v", id, phEff, ph)
		}
	}
}

// 采样时间和限值生效时间用不同时区表示同一真实时刻时，边界判定必须只认真实时刻：
// 边界前一纳秒仍用旧版 10 达标；恰好生效与后一纳秒无论用哪个时区表示，
// 都采用新版 5 并超标，保存的生效时间依据与采样时刻是同一真实时刻。
func TestConfirmNanosecondBoundaryAcrossTimeZones(t *testing.T) {
	boundary := turbBoundary()
	oldEff := at(1, 0)
	beijing := time.FixedZone("CST", 8*3600)
	west5 := time.FixedZone("W5", -5*3600)
	eastHalf := time.FixedZone("E5h30", 5*3600+30*60)

	cases := []struct {
		name                  string
		sampleZone, limitZone *time.Location
	}{
		{"采样UTC_限值北京", time.UTC, beijing},
		{"采样北京_限值UTC", beijing, time.UTC},
		{"采样西五区_限值东五半", west5, eastHalf},
		{"采样与限值都用UTC对照", time.UTC, time.UTC},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := open(t)
			mustPoint(t, s, "P1", "取水口")
			mustLimit(t, s, "P1", flatItem, 6, oldEff)
			mustLimit(t, s, "P1", turbItem, 10, oldEff)
			// 新版生效时间用 limitZone 表示 boundary 这一真实时刻。
			mustLimit(t, s, "P1", turbItem, 5, boundary.In(c.limitZone))

			instants := map[string]time.Time{
				"S-BEFORE": boundary.Add(-1),
				"S-AT":     boundary,
				"S-AFTER":  boundary.Add(1),
			}
			measure := func() []Measurement {
				return []Measurement{{Item: turbItem, Value: 7}, {Item: flatItem, Value: 6}}
			}
			// 采样时间统一用 sampleZone 表示同一真实时刻。
			for _, id := range []string{"S-BEFORE", "S-AT", "S-AFTER"} {
				mustSample(t, s, id, "P1", instants[id].In(c.sampleZone), measure()...)
			}
			for _, id := range []string{"S-BEFORE", "S-AT", "S-AFTER"} {
				smp, err := s.Confirm(id)
				if err != nil {
					t.Fatalf("Confirm %s: %v", id, err)
				}
				switch id {
				case "S-BEFORE":
					checkBoundarySample(t, smp, id, instants[id], 10, oldEff, oldEff, "跨时区-前一纳秒")
				default:
					checkBoundarySample(t, smp, id, instants[id], 5, boundary, oldEff, "跨时区-"+id)
					r := rsByItem(smp.Results)[turbItem]
					// 保存的生效依据必须与任一时区写法表示的都是同一真实时刻。
					if !r.LimitEffective.Equal(boundary.In(c.limitZone)) {
						t.Fatalf("%s: 保存的生效时间与限值登记时刻不是同一真实时刻: %s vs %s",
							id, r.LimitEffective, boundary.In(c.limitZone))
					}
				}
			}
			// 时区写法不能改变按真实时刻晚到早的排列与结论。
			list, err := s.ListByPoint("P1")
			if err != nil || len(list) != 3 {
				t.Fatalf("ListByPoint: %+v err=%v", list, err)
			}
			wantOrder := []string{"S-AFTER", "S-AT", "S-BEFORE"}
			for i, id := range wantOrder {
				if list[i].ID != id {
					t.Fatalf("跨时区排列应为 %v，实际 %+v", wantOrder, list)
				}
			}
		})
	}
}

func rsByItem(rs []ItemResult) map[string]ItemResult {
	m := make(map[string]ItemResult, len(rs))
	for _, r := range rs {
		m[r.Item] = r
	}
	return m
}
