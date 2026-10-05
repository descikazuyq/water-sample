package water

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件保护“打开本地数据”时对已保存记录的完整性校验：
// 任何状态的样品，原测量中同一项目名都只能出现一次（与记录是否相邻、
// 数值是否相同无关，两个零也是重复；不同项目都测得零则正常），待判定样品
// 与从待判定直接作废、没有历史依据的样品也不例外；带判定结论的样品还要求
// 原测量项目与逐项判定按项目名完整一一对应、且对应项目的测量值一致。
// 缺项、多项、项目重复、测量值对不上（零也是合法测量值）或整份样品没有
// 测量项目，都必须让整次 Open 失败，返回 nil 存放，错误信息点到具体样品
// 编号与项目，且不覆盖原文件、不静默跳过、不选取或合并重复测量、不补判定、
// 不改成待判定。待判定样品没有判定记录、待判定后作废（无历史依据）的样品
// 不被要求已有判定依据；已确认后作废但历史依据完整的样品继续兼容。

const (
	openPH   = "pH"
	openTurb = "浊度"
)

func openEff() time.Time { return at(1, 0) }

// writeDiskFile 手工写入一份数据文件，便于构造正常流程不会产生的损坏记录。
func writeDiskFile(t *testing.T, dir string, st diskState) {
	t.Helper()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatalf("marshal data file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "water-data.json"), data, 0o644); err != nil {
		t.Fatalf("write data file: %v", err)
	}
}

func openPoints() map[string]SamplingPoint {
	return map[string]SamplingPoint{"P1": {ID: "P1", Name: "一号取水口"}}
}

// phTurbResults 是与 pH 9、浊度 4 的原测量完整对应的两条判定，
// 排列顺序故意与测量相反，用来证明按项目名对应而非按位置对应。
func phTurbResults() []ItemResult {
	return []ItemResult{
		{Item: openTurb, Value: 4, Limit: 4, LimitEffective: openEff(), Exceeded: false},
		{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
	}
}

func phTurbMeasurements() []Measurement {
	return []Measurement{
		{Item: openPH, Value: 9},
		{Item: openTurb, Value: 4},
	}
}

// assertOpenRejects 断言打开整次失败：返回 nil 存放、错误可识别为
// ErrCorruptRecord，且信息包含出问题的样品编号与项目（noItem 为 true 时
// 只要求编号），原文件字节不被改动。
func assertOpenRejects(t *testing.T, dir, sampleID, itemHint string, noItem bool) {
	t.Helper()
	before, err := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if err != nil {
		t.Fatalf("read file before open: %v", err)
	}
	got, err := Open(dir)
	if err == nil {
		if got != nil {
			got.Close()
		}
		t.Fatalf("损坏数据必须让整次 Open 失败，sample=%s", sampleID)
	}
	if got != nil {
		t.Fatalf("打开失败时不得返回可用的数据存放对象，got %#v", got)
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, sampleID) {
		t.Fatalf("错误信息应指出样品编号 %q，实际 %q", sampleID, msg)
	}
	if !noItem && itemHint != "" && !strings.Contains(msg, itemHint) {
		t.Fatalf("错误信息应指出项目 %q，实际 %q", itemHint, msg)
	}
	after, rerr := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if rerr != nil {
		t.Fatalf("read file after open: %v", rerr)
	}
	if string(after) != string(before) {
		t.Fatalf("打开失败不得覆盖或改写原数据文件")
	}
}

// 已确认样品只剩一个项目的判定依据（浊度缺失）：即使整份超标标记还在，
// 也不能凭该标记返回达标或超标，整次打开失败并点名样品与缺失项目。
func TestOpenConfirmedMissingItemResult(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: phTurbMeasurements(),
				Status:       StatusConfirmed, Exceeded: true,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
				},
			},
		},
	})
	assertOpenRejects(t, dir, "S1", openTurb, false)
}

// 两条判定都在，但浊度判定里的测量值写成了 3（原测量是 4）：
// 项目名称相同也不能接受来自另一份记录的测量值，整次打开失败。
func TestOpenConfirmedMismatchedValue(t *testing.T) {
	dir := t.TempDir()
	results := phTurbResults()
	results[0].Value = 3 // 浊度 4 被写成 3
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: phTurbMeasurements(),
				Status:       StatusConfirmed, Exceeded: false,
				Results: results,
			},
		},
	})
	assertOpenRejects(t, dir, "S1", openTurb, false)
}

// 零是合法测量值：原测量与判定都是零时必须正常读入；只有判定值被改成
// 非零（对不上）时才拒绝，不能把零当成没有填写。
func TestOpenConfirmedZeroValueIsValid(t *testing.T) {
	dir := t.TempDir()
	zeroResults := []ItemResult{
		{Item: openTurb, Value: 0, Limit: 4, LimitEffective: openEff(), Exceeded: false},
		{Item: openPH, Value: 0, Limit: 8, LimitEffective: openEff(), Exceeded: false},
	}

	// 完整且为零：成功读入，零值原样保留。
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 0}, {Item: openTurb, Value: 0}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: zeroResults,
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("零是合法测量值，完整记录应正常读入: %v", err)
	}
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	ms := map[string]float64{}
	for _, m := range list[0].Measurements {
		ms[m.Item] = m.Value
	}
	if ms[openPH] != 0 || ms[openTurb] != 0 {
		t.Fatalf("零测量值必须原样保留: %+v", list[0].Measurements)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 判定把零改成 1：对不上，整次失败；错误仍点名该项目。
	bad := []ItemResult{
		zeroResults[0],
		{Item: openPH, Value: 1, Limit: 8, LimitEffective: openEff(), Exceeded: false},
	}
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 0}, {Item: openTurb, Value: 0}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: bad,
			},
		},
	})
	assertOpenRejects(t, dir, "S1", openPH, false)
}

// 同一项目在原测量里出现两次，是记录有误：整次打开失败。
func TestOpenConfirmedDuplicateMeasurement(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{
					{Item: openPH, Value: 9}, {Item: openPH, Value: 7},
				},
				Status: StatusConfirmed, Exceeded: true,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
				},
			},
		},
	})
	assertOpenRejects(t, dir, "S1", openPH, false)
}

// 同一项目在判定中出现两次，同样是记录有误：整次打开失败。
func TestOpenConfirmedDuplicateResult(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusConfirmed, Exceeded: true,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
					{Item: openPH, Value: 9, Limit: 6, LimitEffective: openEff(), Exceeded: true},
				},
			},
		},
	})
	assertOpenRejects(t, dir, "S1", openPH, false)
}

// 判定中出现原测量没有的项目：整次打开失败并点名多出的项目。
func TestOpenConfirmedExtraResultItem(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusConfirmed, Exceeded: true,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
					{Item: "COD", Value: 1, Limit: 30, LimitEffective: openEff(), Exceeded: false},
				},
			},
		},
	})
	assertOpenRejects(t, dir, "S1", "COD", false)
}

// 已确认样品一个测量项目都没有时也要失败，错误指出是哪份样品。
func TestOpenConfirmedNoMeasurements(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Status: StatusConfirmed, Exceeded: false,
				Results: nil,
			},
		},
	})
	assertOpenRejects(t, dir, "S1", "", true)
}

// 一个文件里其他样品正常，也不能静默跳过有问题的样品继续打开：
// 整次失败、无可用存放，正常样品也读不到。
func TestOpenOneBadSampleRejectsWholeFile(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			// 完整、正常的已确认样品。
			"S-GOOD": {
				ID: "S-GOOD", PointID: "P1", SampledAt: at(5, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openPH, Value: 7, Limit: 8, LimitEffective: openEff(), Exceeded: false},
				},
			},
			// 缺浊度判定的损坏样品。
			"S-BAD": {
				ID: "S-BAD", PointID: "P1", SampledAt: at(10, 0),
				Measurements: phTurbMeasurements(),
				Status:       StatusConfirmed, Exceeded: true,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
				},
			},
		},
	})
	assertOpenRejects(t, dir, "S-BAD", openTurb, false)
}

// 完整记录按项目名对应：两个列表排列顺序不同不影响读入；读入后测量与判定
// 仍各自保留原有顺序，已保存的上限、生效时间、逐项结论与整份超标标记原样保留。
func TestOpenCompleteRecordOrderPreserved(t *testing.T) {
	dir := t.TempDir()
	wantMeas := phTurbMeasurements() // pH 在前、浊度在后
	wantResults := phTurbResults()   // 浊度在前、pH 在后（故意相反）
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: wantMeas,
				Status:       StatusConfirmed, Exceeded: true,
				Results: wantResults,
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("完整记录（顺序相反）应正常读入: %v", err)
	}
	defer s.Close()

	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("最近有效结果应指向 S1: %+v ok=%v err=%v", latest, ok, err)
	}
	// 原测量顺序保留。
	if len(latest.Measurements) != 2 ||
		latest.Measurements[0].Item != openPH || latest.Measurements[0].Value != 9 ||
		latest.Measurements[1].Item != openTurb || latest.Measurements[1].Value != 4 {
		t.Fatalf("原测量顺序必须保留: %+v", latest.Measurements)
	}
	// 判定顺序保留。
	if len(latest.Results) != 2 ||
		latest.Results[0].Item != openTurb || latest.Results[0].Value != 4 ||
		latest.Results[1].Item != openPH || latest.Results[1].Value != 9 {
		t.Fatalf("逐项判定顺序必须保留: %+v", latest.Results)
	}
	// 已保存的上限、生效时间、逐项结论与整份超标标记原样保留，不重新计算。
	turb := latest.Results[0]
	if turb.Limit != 4 || !turb.LimitEffective.Equal(openEff()) || turb.Exceeded {
		t.Fatalf("浊度判定依据应原样保留: %+v", turb)
	}
	ph := latest.Results[1]
	if ph.Limit != 8 || !ph.LimitEffective.Equal(openEff()) || !ph.Exceeded {
		t.Fatalf("pH 判定依据应原样保留: %+v", ph)
	}
	if !latest.Exceeded {
		t.Fatalf("整份超标标记应原样保留为 true")
	}
	// 重复确认返回同一份已保存依据。
	again, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("重复确认: %v", err)
	}
	if len(again.Results) != 2 || again.Results[0].Item != openTurb ||
		again.Results[1].Item != openPH || !again.Exceeded {
		t.Fatalf("重复确认应返回原保存依据: %+v", again.Results)
	}
}

// 后来新增或补录的限值即使会改变重新判定的结果，也不能据此拒绝完整记录，
// 更不能在打开时重算结论：已保存的上限、生效时间与超标结论保持原样。
func TestOpenCompleteRecordUnaffectedByNewerLimits(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "一号取水口")
	mustLimit(t, s, "P1", openPH, 8.0, openEff())
	mustSample(t, s, "S1", "P1", at(10, 0), Measurement{Item: openPH, Value: 9})
	conf, err := s.Confirm("S1")
	if err != nil || !conf.Exceeded || conf.Results[0].Limit != 8.0 {
		t.Fatalf("setup: 9 > 8 应超标: %+v err=%v", conf, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 补录一版采样时刻之后才生效、更宽松的限值；再补一版采样之前生效的
	// 宽松限值（若重算就会变成达标）。
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen for backfill: %v", err)
	}
	mustLimit(t, s2, "P1", openPH, 100.0, at(15, 0))
	mustLimit(t, s2, "P1", openPH, 100.0, at(2, 0))
	if err := s2.Close(); err != nil {
		t.Fatalf("Close s2: %v", err)
	}

	// 重新打开：完整记录不被新限值拒绝，结论仍是保存时的 8@09-01、超标。
	s3, err := Open(dir)
	if err != nil {
		t.Fatalf("含新增限值时重新打开完整记录不应失败: %v", err)
	}
	defer s3.Close()
	latest, ok, err := s3.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
	if !latest.Exceeded || len(latest.Results) != 1 {
		t.Fatalf("整份结论被重算: %+v", latest)
	}
	r := latest.Results[0]
	if r.Value != 9 || r.Limit != 8.0 || !r.LimitEffective.Equal(openEff()) || !r.Exceeded {
		t.Fatalf("已保存依据必须原样保留，不能按新限值重算: %+v", r)
	}
}

// 待判定样品没有判定记录、待判定后作废的样品没有历史依据，都仍是正常情况；
// 已确认后作废但历史依据完整的样品也继续兼容（列表可查、最近结果跳过、
// 再确认拒绝）。
func TestOpenPendingAndVoidedSamples(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: map[string]SamplingPoint{
			"P1": {ID: "P1", Name: "一号取水口"},
			"P2": {ID: "P2", Name: "二号取水口"},
		},
		Samples: map[string]*Sample{
			// 待判定：无判定记录。
			"SP": {
				ID: "SP", PointID: "P1", SampledAt: at(2, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusPending,
			},
			// 待判定后作废：无历史依据。
			"SV": {
				ID: "SV", PointID: "P1", SampledAt: at(3, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusVoided, VoidReason: "录入信息有误",
			},
			// 已确认后作废：历史依据完整。
			"SCV": {
				ID: "SCV", PointID: "P2", SampledAt: at(4, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusVoided, VoidReason: "复测确认样品污染", Exceeded: true,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
				},
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("待判定与无历史依据的作废样品都应正常读入: %v", err)
	}
	defer s.Close()

	p1, err := s.ListByPoint("P1")
	if err != nil || len(p1) != 2 {
		t.Fatalf("P1 列表: %+v err=%v", p1, err)
	}
	byID := map[string]Sample{}
	for _, smp := range p1 {
		byID[smp.ID] = smp
	}
	sp := byID["SP"]
	if sp.Status != StatusPending || sp.Results != nil || sp.Exceeded {
		t.Fatalf("待判定样品读入后不应带结论: %+v", sp)
	}
	sv := byID["SV"]
	if sv.Status != StatusVoided || sv.VoidReason != "录入信息有误" ||
		len(sv.Results) != 0 || sv.Exceeded {
		t.Fatalf("待判定后作废样品不应凭空获得结论: %+v", sv)
	}
	// P1 没有已确认且未作废的样品：明确无结果。
	if latest, ok, err := s.LatestResult("P1"); err != nil || ok {
		t.Fatalf("P1 应无最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}

	// 已确认后作废的完整历史记录继续兼容。
	p2, err := s.ListByPoint("P2")
	if err != nil || len(p2) != 1 {
		t.Fatalf("P2 列表: %+v err=%v", p2, err)
	}
	scv := p2[0]
	if scv.Status != StatusVoided || !scv.Exceeded || len(scv.Results) != 1 {
		t.Fatalf("已确认后作废的历史依据应保留: %+v", scv)
	}
	if r := scv.Results[0]; r.Item != openPH || r.Value != 9 || r.Limit != 8 ||
		!r.LimitEffective.Equal(openEff()) || !r.Exceeded {
		t.Fatalf("历史判定依据被改动: %+v", r)
	}
	if latest, ok, err := s.LatestResult("P2"); err != nil || ok {
		t.Fatalf("作废样品不应作为最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
	if _, err := s.Confirm("SCV"); !errors.Is(err, ErrVoided) {
		t.Fatalf("已作废样品再确认应被拒绝，got %v", err)
	}
}

// 已确认后作废但历史依据缺项，同样不能读入：作废只取消有效资格，
// 不能让对不上的结论借“历史记录”名义混进台账。
func TestOpenVoidedAfterConfirmedMissingItemResult(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: phTurbMeasurements(),
				Status:       StatusVoided, VoidReason: "作废", Exceeded: true,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
				},
			},
		},
	})
	assertOpenRejects(t, dir, "S1", openTurb, false)
}

// 文件里出现空样品记录（JSON null）时不能崩溃，也要整次失败并按 map 编号指出。
func TestOpenNullSampleRecord(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": nil,
		},
	})
	assertOpenRejects(t, dir, "S1", "", true)
}

// 待判定样品的原测量里同一项目出现两次（两条 pH 都是零）：不能因为它还没有
// 逐项判定就放行进台账，否则一旦存在适用上限，确认时会把两条测量都保存成
// 判定依据。整次打开必须失败，点名样品与项目，原文件保持原样。
func TestOpenPendingDuplicateMeasurementZeroValues(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		// 即使文件里已有适用上限，重复测量也要在确认之前、打开当时被拦下。
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {{PointID: "P1", Item: openPH, Value: 8, Effective: openEff()}},
		},
		Samples: map[string]*Sample{
			"SP": {
				ID: "SP", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{
					{Item: openPH, Value: 0}, {Item: openPH, Value: 0},
				},
				Status: StatusPending,
			},
		},
	})
	assertOpenRejects(t, dir, "SP", openPH, false)
}

// 从待判定直接作废、没有历史判定依据的样品，原测量项目重复同样在打开时拦下。
func TestOpenVoidedWithoutResultsDuplicateMeasurement(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"SV": {
				ID: "SV", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{
					{Item: openPH, Value: 9},
					{Item: openTurb, Value: 4},
					{Item: openPH, Value: 3}, // 与第一条不相邻、数值也不同
				},
				Status: StatusVoided, VoidReason: "录入信息有误",
			},
		},
	})
	assertOpenRejects(t, dir, "SV", openPH, false)
}

// 重复判断只看完整项目名：pH 与浊度都测得零是两个不同项目，不能误拒绝；
// 同一份待判定样品因此正常读入，且不会凭空获得判定依据。
func TestOpenPendingDifferentItemsBothZeroAllowed(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"SP": {
				ID: "SP", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 0}, {Item: openTurb, Value: 0}},
				Status:       StatusPending,
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("不同项目都测得零不是重复，应正常读入: %v", err)
	}
	defer s.Close()
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	smp := list[0]
	if smp.Status != StatusPending || smp.Results != nil || smp.Exceeded {
		t.Fatalf("待判定样品读入后不应带结论: %+v", smp)
	}
	got := map[string]float64{}
	for _, m := range smp.Measurements {
		got[m.Item] = m.Value
	}
	if len(got) != 2 || got[openPH] != 0 || got[openTurb] != 0 {
		t.Fatalf("两条不同项目的零测量应原样保留: %+v", smp.Measurements)
	}
}

// 同一项目出现在不同样品中是正常记录：即使两份样品都有 pH（数值相同也算），
// 打开仍成功；只有一份样品内部出现两条同名测量才是损坏。
func TestOpenSameItemAcrossDifferentSamplesAllowed(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 0}},
				Status:       StatusPending,
			},
			"S2": {
				ID: "S2", PointID: "P1", SampledAt: at(11, 0),
				Measurements: []Measurement{{Item: openPH, Value: 0}, {Item: openTurb, Value: 0}},
				Status:       StatusVoided, VoidReason: "录入信息有误",
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("同一项目分属不同样品是正常记录: %v", err)
	}
	defer s.Close()
	if list, err := s.ListByPoint("P1"); err != nil || len(list) != 2 {
		t.Fatalf("两份样品都应读入: %+v err=%v", list, err)
	}
}

// 文件里另有多份完整样品，也不能只跳过带重复测量的待判定样品继续打开：
// 整次失败、返回 nil 存放。
func TestOpenPendingDuplicateRejectsWholeFile(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			// 完整、正常的已确认样品。
			"S-GOOD": {
				ID: "S-GOOD", PointID: "P1", SampledAt: at(5, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openPH, Value: 7, Limit: 8, LimitEffective: openEff(), Exceeded: false},
				},
			},
			// 原测量项目重复的待判定样品。
			"S-BAD": {
				ID: "S-BAD", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{
					{Item: openTurb, Value: 1},
					{Item: openPH, Value: 9},
					{Item: openTurb, Value: 1}, // 不相邻、数值相同仍是重复
				},
				Status: StatusPending,
			},
		},
	})
	assertOpenRejects(t, dir, "S-BAD", openTurb, false)
}

// 端到端复现修复点：正常写入的待判定样品重开后仍可确认，逐项依据恰好一条；
// 而被外部改成两条同名测量的文件根本无法 Open，确认流程接触不到损坏记录。
func TestOpenPendingDuplicateBlocksConfirm(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "一号取水口")
	mustLimit(t, s, "P1", openPH, 8.0, openEff())
	mustLimit(t, s, "P1", openTurb, 4.0, openEff())
	mustSample(t, s, "S1", "P1", at(10, 0),
		Measurement{Item: openPH, Value: 9}, Measurement{Item: openTurb, Value: 4})
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 正常文件：重开后确认成功，每个项目恰好一条判定依据。
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("正常待判定样品应能重开: %v", err)
	}
	conf, err := s2.Confirm("S1")
	if err != nil {
		t.Fatalf("正常样品确认: %v", err)
	}
	if len(conf.Results) != 2 {
		t.Fatalf("每个项目应恰好一条判定依据: %+v", conf.Results)
	}
	if err := s2.Close(); err != nil {
		t.Fatalf("Close s2: %v", err)
	}

	// 外部改动：给待判定样品再塞一条同名 pH（两条数值相同）。
	var st diskState
	raw, err := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("unmarshal data file: %v", err)
	}
	st.Samples["S1"].Status = StatusPending
	st.Samples["S1"].Results = nil
	st.Samples["S1"].Exceeded = false
	st.Samples["S1"].Measurements = append(st.Samples["S1"].Measurements,
		Measurement{Item: openPH, Value: 9})
	writeDiskFile(t, dir, st)

	got, err := Open(dir)
	if err == nil {
		if got != nil {
			got.Close()
		}
		t.Fatalf("被外部塞入重复测量的文件必须整次 Open 失败")
	}
	if got != nil {
		t.Fatalf("打开失败时不得返回可用的数据存放对象，got %#v", got)
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，got %v", err)
	}
}
