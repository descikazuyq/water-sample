package water

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// 本文件只针对一件事：打开本地数据时，已确认（含确认后作废）样品必须带着
// 与原测量项目完整对应的逐项判定才能读入——每份至少一个测量项目，每个项目
// 有且仅有一条判定，判定中不能出现原测量没有的项目，任一侧项目重复、对应
// 测量值与原记录不一致，或缺项，整次打开都必须失败，且不提供可用的存放对象。
// 待判定样品、未确认即作废的样品没有判定依据，属正常情况；完整记录原样读入，
// 后补的限值不影响已保存依据。

// corruptOpenDir 把给定磁盘状态写入一个全新临时目录的数据文件，返回该目录。
func corruptOpenDir(t *testing.T, st diskState) string {
	t.Helper()
	dir := t.TempDir()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatalf("marshal data file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "water-data.json"), data, 0o644); err != nil {
		t.Fatalf("write data file: %v", err)
	}
	return dir
}

// assertOpenRejectsCorrupt 断言打开该目录整体失败：错误可被
// ErrCorruptRecord 识别、不返回可用存放、错误信息点到样品编号与项目，
// 且数据文件原样保留（不补判定、不改状态、不覆盖）。
func assertOpenRejectsCorrupt(t *testing.T, dir, sampleID, itemHint string) []byte {
	t.Helper()
	before, err := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if err != nil {
		t.Fatalf("read data file before open: %v", err)
	}
	s, err := Open(dir)
	if err == nil {
		s.Close()
		t.Fatalf("损坏记录必须让整次打开失败，却成功返回了存放")
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，实际 %v", err)
	}
	if s != nil {
		t.Fatalf("打开失败时不应提供可用的数据存放对象，实际 %+v", s)
	}
	if sampleID != "" && !strings.Contains(err.Error(), sampleID) {
		t.Fatalf("错误信息应指出出问题的样品编号 %q，实际 %v", sampleID, err)
	}
	if itemHint != "" && !strings.Contains(err.Error(), itemHint) {
		t.Fatalf("错误信息应指出出问题的项目 %q，实际 %v", itemHint, err)
	}
	after, rerr := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if rerr != nil {
		t.Fatalf("read data file after open: %v", rerr)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("打开失败不得改动或覆盖原数据文件")
	}
	return after
}

// twoItemConfirmedDisk 构造一份 pH 9、浊度 4 的双项目已确认样品磁盘状态，
// 判定与测量完整对应（pH 9 > 8 超标，浊度 4 == 4 达标）。
func twoItemConfirmedDisk(id string, measurements []Measurement, results []ItemResult, status Status, reason string) diskState {
	eff := at(1, 0)
	return diskState{
		Points: map[string]SamplingPoint{"P1": {ID: "P1", Name: "取水口"}},
		Limits: map[string][]Limit{
			diskLimitKey("P1", "pH"):   {{PointID: "P1", Item: "pH", Value: 8, Effective: eff}},
			diskLimitKey("P1", "浊度"): {{PointID: "P1", Item: "浊度", Value: 4, Effective: eff}},
		},
		Samples: map[string]*Sample{
			id: {
				ID: id, PointID: "P1", SampledAt: at(10, 0),
				Measurements: measurements,
				Status:       status, Exceeded: true, VoidReason: reason,
				Results: results,
			},
		},
	}
}

func goodTwoItemMeasurements() []Measurement {
	return []Measurement{{Item: "pH", Value: 9}, {Item: "浊度", Value: 4}}
}

func goodTwoItemResults() []ItemResult {
	eff := at(1, 0)
	return []ItemResult{
		{Item: "pH", Value: 9, Limit: 8, LimitEffective: eff, Exceeded: true},
		{Item: "浊度", Value: 4, Limit: 4, LimitEffective: eff, Exceeded: false},
	}
}

// 只保存了部分项目的判定：测量有 pH、浊度两项，判定只剩 pH 一项。
// 不能只凭整份超标标记把它当成已确认记录，打开必须失败并点名样品与缺失项目。
func TestOpenConfirmedMissingItemResultRejected(t *testing.T) {
	st := twoItemConfirmedDisk("S-PARTIAL", goodTwoItemMeasurements(),
		goodTwoItemResults()[:1], StatusConfirmed, "")
	dir := corruptOpenDir(t, st)
	assertOpenRejectsCorrupt(t, dir, "S-PARTIAL", "浊度")

	// 不修复文件就再次打开，仍失败：不能有任何一次“带病打开”。
	assertOpenRejectsCorrupt(t, dir, "S-PARTIAL", "浊度")
}

// 判定项目齐全但其中一条的测量值与原记录不一致（浊度原测量 4 写成 3）：
// 不能接受项目同名却属于另一份记录的判定，打开失败并点名该项目。
func TestOpenConfirmedMismatchedValueRejected(t *testing.T) {
	results := goodTwoItemResults()
	results[1].Value = 3
	st := twoItemConfirmedDisk("S-MISMATCH", goodTwoItemMeasurements(), results, StatusConfirmed, "")
	dir := corruptOpenDir(t, st)
	assertOpenRejectsCorrupt(t, dir, "S-MISMATCH", "浊度")
}

// 判定里出现原测量没有的项目（多项），同样破坏一一对应，整次打开失败。
func TestOpenConfirmedExtraResultItemRejected(t *testing.T) {
	eff := at(1, 0)
	measurements := []Measurement{{Item: "pH", Value: 9}}
	results := []ItemResult{
		{Item: "pH", Value: 9, Limit: 8, LimitEffective: eff, Exceeded: true},
		{Item: "余氯", Value: 0, Limit: 1, LimitEffective: eff, Exceeded: false},
	}
	st := twoItemConfirmedDisk("S-EXTRA", measurements, results, StatusConfirmed, "")
	dir := corruptOpenDir(t, st)
	assertOpenRejectsCorrupt(t, dir, "S-EXTRA", "余氯")
}

// 同一项目在原测量中出现两次：记录有误，即使判定数量看似吻合也必须拒绝。
func TestOpenDuplicateMeasurementRejected(t *testing.T) {
	eff := at(1, 0)
	measurements := []Measurement{{Item: "pH", Value: 9}, {Item: "pH", Value: 7}}
	results := []ItemResult{
		{Item: "pH", Value: 9, Limit: 8, LimitEffective: eff, Exceeded: true},
	}
	st := twoItemConfirmedDisk("S-DUP-M", measurements, results, StatusConfirmed, "")
	dir := corruptOpenDir(t, st)
	assertOpenRejectsCorrupt(t, dir, "S-DUP-M", "pH")
}

// 同一项目在逐项判定中出现两次：记录有误，整次打开失败。
func TestOpenDuplicateResultRejected(t *testing.T) {
	eff := at(1, 0)
	measurements := goodTwoItemMeasurements()
	results := []ItemResult{
		{Item: "pH", Value: 9, Limit: 8, LimitEffective: eff, Exceeded: true},
		{Item: "pH", Value: 9, Limit: 8, LimitEffective: eff, Exceeded: true},
	}
	st := twoItemConfirmedDisk("S-DUP-R", measurements, results, StatusConfirmed, "")
	dir := corruptOpenDir(t, st)
	assertOpenRejectsCorrupt(t, dir, "S-DUP-R", "pH")
}

// 已确认样品一个测量项目都没有时，打开失败并指出是哪份样品；
// 已确认却连一条判定都没有，同样按缺项拒绝。
func TestOpenConfirmedWithoutMeasurementsRejected(t *testing.T) {
	eff := at(1, 0)
	st := diskState{
		Points: map[string]SamplingPoint{"P1": {ID: "P1", Name: "取水口"}},
		Samples: map[string]*Sample{
			"S-EMPTY": {
				ID: "S-EMPTY", PointID: "P1", SampledAt: at(10, 0),
				Status: StatusConfirmed, Exceeded: false,
				Results: []ItemResult{{Item: "pH", Value: 9, Limit: 8, LimitEffective: eff, Exceeded: true}},
			},
		},
	}
	dir := corruptOpenDir(t, st)
	assertOpenRejectsCorrupt(t, dir, "S-EMPTY", "")

	st2 := diskState{
		Points: map[string]SamplingPoint{"P1": {ID: "P1", Name: "取水口"}},
		Samples: map[string]*Sample{
			"S-NORESULT": {
				ID: "S-NORESULT", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: "pH", Value: 9}},
				Status:       StatusConfirmed,
			},
		},
	}
	dir2 := corruptOpenDir(t, st2)
	assertOpenRejectsCorrupt(t, dir2, "S-NORESULT", "pH")
}

// 确认后作废的历史记录同样必须依据完整：缺项或测量值对不上时，
// 不能借“作废只是历史台账”的名义带病读入。
func TestOpenVoidedConfirmedBasisAlsoValidated(t *testing.T) {
	// 缺项的确认后作废记录
	st := twoItemConfirmedDisk("S-VOID-PARTIAL", goodTwoItemMeasurements(),
		goodTwoItemResults()[:1], StatusVoided, "采样瓶破损")
	dir := corruptOpenDir(t, st)
	assertOpenRejectsCorrupt(t, dir, "S-VOID-PARTIAL", "浊度")

	// 测量值对不上的确认后作废记录
	results := goodTwoItemResults()
	results[1].Value = 3
	st2 := twoItemConfirmedDisk("S-VOID-MISMATCH", goodTwoItemMeasurements(),
		results, StatusVoided, "采样瓶破损")
	dir2 := corruptOpenDir(t, st2)
	assertOpenRejectsCorrupt(t, dir2, "S-VOID-MISMATCH", "浊度")
}

// 测量值为零是合法值：余氯 0 的测量与判定都在时正常读入，
// 缺了它的判定仍按缺项拒绝，零不能被当成“没有填写”。
func TestOpenZeroMeasurementIsAValidValue(t *testing.T) {
	eff := at(1, 0)
	good := diskState{
		Points: map[string]SamplingPoint{"P1": {ID: "P1", Name: "取水口"}},
		Limits: map[string][]Limit{
			diskLimitKey("P1", "余氯"): {{PointID: "P1", Item: "余氯", Value: 1, Effective: eff}},
		},
		Samples: map[string]*Sample{
			"S-ZERO": {
				ID: "S-ZERO", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: "余氯", Value: 0}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{{Item: "余氯", Value: 0, Limit: 1, LimitEffective: eff, Exceeded: false}},
			},
		},
	}
	dir := corruptOpenDir(t, good)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("零值测量与判定完整对应时应正常打开: %v", err)
	}
	list, lerr := s.ListByPoint("P1")
	if lerr != nil || len(list) != 1 || list[0].Results[0].Value != 0 {
		t.Fatalf("零值应原样读入: %+v err=%v", list, lerr)
	}
	s.Close()

	// 同一份测量里余氯 0，却没有余氯的判定：仍按缺项拒绝。
	missing := diskState{
		Points: map[string]SamplingPoint{"P1": {ID: "P1", Name: "取水口"}},
		Samples: map[string]*Sample{
			"S-ZERO-MISS": {
				ID: "S-ZERO-MISS", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: "余氯", Value: 0}},
				Status:       StatusConfirmed,
			},
		},
	}
	dir2 := corruptOpenDir(t, missing)
	assertOpenRejectsCorrupt(t, dir2, "S-ZERO-MISS", "余氯")
}

// 完整记录原样读入：项目排列在测量与判定两个列表里各自保留
// （这里测量浊度在前、判定 pH 在前），上限、生效时间、逐项结论与整份
// 超标标记都不变。
func TestOpenCompleteBasisReadAsSaved(t *testing.T) {
	eff := at(1, 0)
	measurements := []Measurement{{Item: "浊度", Value: 4}, {Item: "pH", Value: 9}}
	results := []ItemResult{
		{Item: "pH", Value: 9, Limit: 8, LimitEffective: eff, Exceeded: true},
		{Item: "浊度", Value: 4, Limit: 4, LimitEffective: eff, Exceeded: false},
	}
	st := twoItemConfirmedDisk("S-OK", measurements, results, StatusConfirmed, "")
	dir := corruptOpenDir(t, st)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("完整记录应正常打开: %v", err)
	}
	defer s.Close()
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	got := list[0]
	if got.ID != "S-OK" || got.Status != StatusConfirmed || !got.Exceeded {
		t.Fatalf("状态或整份结论未原样读入: %+v", got)
	}
	if !reflect.DeepEqual(got.Measurements, measurements) {
		t.Fatalf("测量排列应原样保留: got %+v want %+v", got.Measurements, measurements)
	}
	if !reflect.DeepEqual(got.Results, results) {
		t.Fatalf("逐项判定排列与依据应原样保留: got %+v want %+v", got.Results, results)
	}
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S-OK" {
		t.Fatalf("完整记录读入后应能作为最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
	if !reflect.DeepEqual(latest.Results, results) {
		t.Fatalf("最近有效结果的依据应与保存一致: got %+v", latest.Results)
	}
}

// 后来新增或补录的限值即使会改变重新判定的结果，也不能据此拒绝完整记录，
// 更不能重新计算已保存结论；读入的仍是保存时的上限与结论。
func TestOpenDoesNotRecomputeAgainstNewerLimits(t *testing.T) {
	eff := at(1, 0)
	st := twoItemConfirmedDisk("S-KEPT", goodTwoItemMeasurements(), goodTwoItemResults(),
		StatusConfirmed, "")
	// 额外补录一版对采样时刻已适用、宽松到会让 pH 9 达标的新上限。
	st.Limits[diskLimitKey("P1", "pH")] = append(st.Limits[diskLimitKey("P1", "pH")],
		Limit{PointID: "P1", Item: "pH", Value: 100, Effective: at(9, 0)})
	dir := corruptOpenDir(t, st)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("补录限值不应影响完整记录读入: %v", err)
	}
	defer s.Close()
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("最近有效结果应存在: %+v ok=%v err=%v", latest, ok, err)
	}
	ph := latest.Results[0]
	if ph.Limit != 8.0 || !ph.LimitEffective.Equal(eff) || !ph.Exceeded || !latest.Exceeded {
		t.Fatalf("已保存依据不能被新限值重算: %+v", ph)
	}
}

// 待判定样品没有判定记录、待判定后作废的样品没有历史依据，都是正常情况，
// 即使与已确认样品同处一个文件也照常读入。
func TestOpenPendingAndPendingVoidedAreNormal(t *testing.T) {
	eff := at(1, 0)
	st := diskState{
		Points: map[string]SamplingPoint{"P1": {ID: "P1", Name: "取水口"}},
		Limits: map[string][]Limit{
			diskLimitKey("P1", "pH"): {{PointID: "P1", Item: "pH", Value: 8, Effective: eff}},
		},
		Samples: map[string]*Sample{
			"S-PENDING": {
				ID: "S-PENDING", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: "pH", Value: 9}},
				Status:       StatusPending,
			},
			"S-PENDING-VOID": {
				ID: "S-PENDING-VOID", PointID: "P1", SampledAt: at(11, 0),
				Measurements: []Measurement{{Item: "pH", Value: 9}},
				Status:       StatusVoided, VoidReason: "录入信息有误",
			},
			"S-CONFIRMED": {
				ID: "S-CONFIRMED", PointID: "P1", SampledAt: at(12, 0),
				Measurements: []Measurement{{Item: "pH", Value: 9}},
				Status:       StatusConfirmed, Exceeded: true,
				Results: []ItemResult{{Item: "pH", Value: 9, Limit: 8, LimitEffective: eff, Exceeded: true}},
			},
		},
	}
	dir := corruptOpenDir(t, st)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("待判定与未确认即作废记录应正常读入: %v", err)
	}
	defer s.Close()
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 3 {
		t.Fatalf("三份样品都应读入: %+v err=%v", list, err)
	}
	byID := map[string]Sample{}
	for _, smp := range list {
		byID[smp.ID] = smp
	}
	if p := byID["S-PENDING"]; p.Status != StatusPending || p.Results != nil || p.Exceeded {
		t.Fatalf("待判定样品应原样读入且无结论: %+v", p)
	}
	if v := byID["S-PENDING-VOID"]; v.Status != StatusVoided || v.VoidReason != "录入信息有误" ||
		len(v.Results) != 0 || v.Exceeded {
		t.Fatalf("未确认即作废样品应无依据读入: %+v", v)
	}
	// 最近有效结果只取完整的已确认样品。
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S-CONFIRMED" {
		t.Fatalf("最近有效结果应为完整的 S-CONFIRMED: %+v ok=%v err=%v", latest, ok, err)
	}
}

// 一个文件里其他样品都正常，只要有一份已确认样品依据不完整，整次打开就失败，
// 不能静默跳过坏样品后继续使用其余样品。
func TestOpenOneCorruptSampleRejectsWholeFile(t *testing.T) {
	eff := at(1, 0)
	st := diskState{
		Points: map[string]SamplingPoint{"P1": {ID: "P1", Name: "取水口"}},
		Limits: map[string][]Limit{
			diskLimitKey("P1", "pH"):   {{PointID: "P1", Item: "pH", Value: 8, Effective: eff}},
			diskLimitKey("P1", "浊度"): {{PointID: "P1", Item: "浊度", Value: 4, Effective: eff}},
		},
		Samples: map[string]*Sample{
			"S-GOOD": {
				ID: "S-GOOD", PointID: "P1", SampledAt: at(5, 0),
				Measurements: []Measurement{{Item: "pH", Value: 7}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{{Item: "pH", Value: 7, Limit: 8, LimitEffective: eff, Exceeded: false}},
			},
			"S-BAD": {
				ID: "S-BAD", PointID: "P1", SampledAt: at(10, 0),
				Measurements: goodTwoItemMeasurements(),
				Status:       StatusConfirmed, Exceeded: true,
				Results: goodTwoItemResults()[:1], // 缺浊度判定
			},
		},
	}
	dir := corruptOpenDir(t, st)
	assertOpenRejectsCorrupt(t, dir, "S-BAD", "浊度")

	// 修好坏样品（补回缺失判定，不改其它内容）后整份文件即可正常打开，
	// 其他样品仍原样可用。
	data, err := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	var fixed diskState
	if err := json.Unmarshal(data, &fixed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	fixed.Samples["S-BAD"].Results = goodTwoItemResults()
	out, err := json.MarshalIndent(fixed, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixed file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "water-data.json"), out, 0o644); err != nil {
		t.Fatalf("write fixed file: %v", err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("修复后应能正常打开: %v", err)
	}
	defer s.Close()
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 2 {
		t.Fatalf("修复后两份样品都应可用: %+v err=%v", list, err)
	}
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S-BAD" {
		t.Fatalf("最近有效结果应为较晚的 S-BAD: %+v ok=%v err=%v", latest, ok, err)
	}
	if len(latest.Results) != 2 {
		t.Fatalf("补回的判定应完整读入: %+v", latest.Results)
	}
}
