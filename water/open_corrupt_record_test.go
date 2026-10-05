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

// 本文件保护“打开本地数据”时的记录完整性校验：
// 每份样品的原测量中同一个项目名只能出现一次，这条规则对任何状态的样品都生效，
// 包括没有逐项判定的待判定样品，以及待判定后直接作废、没有历史判定依据的样品；
// 重复按保存的完整项目名判断，不靠相邻、不因同值（含两个零）放行。
// 带结论的样品还要求原测量项目与逐项判定按项目名完整一一对应、且对应项目的
// 测量值一致；缺项、多项、判定项目重复、测量值对不上（零也是合法测量值）或
// 整份样品没有测量项目，都必须让整次 Open 失败，返回 nil 存放，错误信息点到
// 具体样品编号与项目，且不覆盖原文件、不静默跳过、不择一保留或合并重复测量、
// 不补判定、不改成待判定。超标标记还必须与这份样品已保存的判定依据一致：
// 每条判定测量值严格大于其保存的上限才应为超标，小于或等于（含等于、零或负数
// 组合）都应为达标；整份标记必须与逐项结论一致，任一超标即为真、全部达标即为
// 假。单项标记相反，或单项都对而整份标记相反，都让整次 Open 失败；不修正标记、
// 不按当前限值重做判定。待判定样品、待判定后作废（无历史依据）的样品在原测量
// 不重复时不受影响；已确认后作废但历史依据完整且标记一致的样品继续兼容。

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

// 待判定样品没有逐项判定不是损坏，但原测量里同一项目出现两次仍是损坏：
// 即使两条 pH 都是零，也必须在打开时整次失败并点名样品与项目，
// 不能等确认时才暴露，更不能把两条测量都当成判定依据。
func TestOpenPendingDuplicateMeasurement(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{
					{Item: openPH, Value: 0}, {Item: openPH, Value: 0},
				},
				Status: StatusPending,
			},
		},
	})
	assertOpenRejects(t, dir, "S1", openPH, false)
}

// 待判定样品的重复项目不要求相邻：pH 中间隔着浊度，仍是同一份样品里的重复。
func TestOpenPendingDuplicateMeasurementNonAdjacent(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{
					{Item: openPH, Value: 9}, {Item: openTurb, Value: 4}, {Item: openPH, Value: 7},
				},
				Status: StatusPending,
			},
		},
	})
	assertOpenRejects(t, dir, "S1", openPH, false)
}

// 待判定后直接作废、没有历史判定依据的样品同样不能带重复测量混入台账。
func TestOpenVoidedFromPendingDuplicateMeasurement(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{
					{Item: openPH, Value: 9}, {Item: openPH, Value: 9},
				},
				Status: StatusVoided, VoidReason: "录入信息有误",
			},
		},
	})
	assertOpenRejects(t, dir, "S1", openPH, false)
}

// 不同项目碰巧测得相同的零值不是重复：待判定样品的 pH 与浊度都是零，
// 必须正常读入，且读入后仍是待判定、没有结论。
func TestOpenPendingDifferentItemsSameZero(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 0}, {Item: openTurb, Value: 0}},
				Status:       StatusPending,
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("不同项目同值（零）不是重复，待判定样品应正常读入: %v", err)
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
	ms := map[string]float64{}
	for _, m := range smp.Measurements {
		ms[m.Item] = m.Value
	}
	if len(ms) != 2 || ms[openPH] != 0 || ms[openTurb] != 0 {
		t.Fatalf("两个不同项目的零测量值必须原样保留: %+v", smp.Measurements)
	}
}

// 同一项目出现在不同样品中是正常记录：两份待判定样品各有一条 pH，必须都读入。
func TestOpenSameItemAcrossDifferentSamples(t *testing.T) {
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
				Measurements: []Measurement{{Item: openPH, Value: 0}},
				Status:       StatusPending,
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("同一项目分属不同样品是正常记录: %v", err)
	}
	defer s.Close()
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 2 {
		t.Fatalf("两份样品都应读入: %+v err=%v", list, err)
	}
}

// 待判定的损坏样品与其他完整样品同处一个文件：不能只跳过它继续打开，
// 整次失败、无可用存放，完整样品也读不到。
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
			// 待判定但测量项目重复的损坏样品。
			"S-BAD": {
				ID: "S-BAD", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{
					{Item: openPH, Value: 0}, {Item: openPH, Value: 0},
				},
				Status: StatusPending,
			},
		},
	})
	assertOpenRejects(t, dir, "S-BAD", openPH, false)
}

// 待判定样品丢失全部原测量（空列表）：即使编号、采样点、采样时间齐全，
// 也不能读入；否则请求确认会得到没有逐项依据、超标标记为假的“已确认”记录，
// 还可能被当成采样点的最近有效结果。
func TestOpenPendingNoMeasurements(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{},
				Status:       StatusPending,
			},
		},
	})
	assertOpenRejects(t, dir, "S1", "", true)
}

// 待判定样品的原测量保存为 JSON null：与空列表一样是内容残缺，整次打开失败。
func TestOpenPendingNilMeasurements(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: nil,
				Status:       StatusPending,
			},
		},
	})
	assertOpenRejects(t, dir, "S1", "", true)
}

// 原测量字段在文件中整个缺失（JSON 反序列化后为 nil），同样必须拒绝。
// 这里直接写出不含 measurements 字段的 JSON，而不是依赖 Go 结构体序列化。
func TestOpenPendingMissingMeasurementsField(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {"id": "S1", "pointId": "P1", "sampledAt": "` +
		at(10, 0).Format(time.RFC3339Nano) + `", "status": "pending"}
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "water-data.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write data file: %v", err)
	}
	assertOpenRejects(t, dir, "S1", "", true)
}

// 待判定后直接作废、没有历史结论的样品丢失全部原测量，同样不能因为它不再
// 参与有效判定就放过内容丢失：整次打开失败。
func TestOpenVoidedFromPendingNoMeasurements(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: nil,
				Status:       StatusVoided, VoidReason: "录入信息有误",
			},
		},
	})
	assertOpenRejects(t, dir, "S1", "", true)
}

// 已作废样品的原测量为空但仍残留逐项判定：不能反过来用判定内容填补原测量，
// 整次打开失败。
func TestOpenVoidedNoMeasurementsWithLeftoverResults(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: nil,
				Status:       StatusVoided, VoidReason: "复测确认样品污染", Exceeded: true,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
				},
			},
		},
	})
	assertOpenRejects(t, dir, "S1", "", true)
}

// 已确认样品原测量为空、只剩残留判定和整份超标标记，不能凭标记取得达标或
// 超标资格：整次打开失败。
func TestOpenConfirmedNoMeasurementsWithLeftoverResults(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: nil,
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openPH, Value: 7, Limit: 8, LimitEffective: openEff(), Exceeded: false},
				},
			},
		},
	})
	assertOpenRejects(t, dir, "S1", "", true)
}

// 一份丢失原测量的待判定样品与其他完整样品同处一个文件：整次失败、无可用
// 存放，完整样品也不能被部分读入；原文件保持原样。
func TestOpenNoMeasurementsRejectsWholeFile(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			// 完整、正常的待判定样品。
			"S-GOOD": {
				ID: "S-GOOD", PointID: "P1", SampledAt: at(5, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusPending,
			},
			// 丢失全部原测量的待判定样品。
			"S-BAD": {
				ID: "S-BAD", PointID: "P1", SampledAt: at(10, 0),
				Measurements: nil,
				Status:       StatusPending,
			},
		},
	})
	assertOpenRejects(t, dir, "S-BAD", "", true)
}

// 只含一个项目且测量值为零的样品并不为空：零值和原状态都必须保留。
func TestOpenSingleZeroMeasurementIsNotEmpty(t *testing.T) {
	for _, c := range []struct {
		name   string
		status Status
		reason string
	}{
		{"待判定", StatusPending, ""},
		{"待判定后作废", StatusVoided, "录入信息有误"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeDiskFile(t, dir, diskState{
				Points: openPoints(),
				Samples: map[string]*Sample{
					"S1": {
						ID: "S1", PointID: "P1", SampledAt: at(10, 0),
						Measurements: []Measurement{{Item: openPH, Value: 0}},
						Status:       c.status, VoidReason: c.reason,
					},
				},
			})
			s, err := Open(dir)
			if err != nil {
				t.Fatalf("单项目零测量不是空样品，应正常读入: %v", err)
			}
			defer s.Close()
			list, err := s.ListByPoint("P1")
			if err != nil || len(list) != 1 {
				t.Fatalf("ListByPoint: %+v err=%v", list, err)
			}
			smp := list[0]
			if len(smp.Measurements) != 1 || smp.Measurements[0].Item != openPH ||
				smp.Measurements[0].Value != 0 {
				t.Fatalf("零测量值必须原样保留: %+v", smp.Measurements)
			}
			if smp.Status != c.status || smp.VoidReason != c.reason {
				t.Fatalf("原状态必须保留: %+v", smp)
			}
		})
	}
}

// 没有任何样品的新数据目录，以及样品集合为空的已有数据文件，都应照常打开；
// 需要拒绝的是已经存在却没有原测量的样品，而不是尚未录入样品的数据存放。
func TestOpenEmptySampleStore(t *testing.T) {
	// 全新目录：数据文件尚不存在。
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("全新数据目录应正常打开: %v", err)
	}
	if list, err := s.ListByPoint("P1"); err != nil || len(list) != 0 {
		t.Fatalf("全新目录应没有样品: %+v err=%v", list, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 已有数据文件但样品集合为空（map 为空）。
	writeDiskFile(t, dir, diskState{
		Points:  openPoints(),
		Samples: map[string]*Sample{},
	})
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("样品集合为空的已有数据应正常打开: %v", err)
	}
	defer s2.Close()
	if list, err := s2.ListByPoint("P1"); err != nil || len(list) != 0 {
		t.Fatalf("空样品集合应没有样品: %+v err=%v", list, err)
	}

	// 已有数据文件但样品字段整体缺失（JSON null）。
	raw := `{"points": {"P1": {"id": "P1", "name": "一号取水口"}}}`
	if err := os.WriteFile(filepath.Join(dir, "water-data.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write data file: %v", err)
	}
	s3, err := Open(dir)
	if err != nil {
		t.Fatalf("样品字段缺失的已有数据应正常打开: %v", err)
	}
	defer s3.Close()
	if list, err := s3.ListByPoint("P1"); err != nil || len(list) != 0 {
		t.Fatalf("样品字段缺失应没有样品: %+v err=%v", list, err)
	}
}

// assertOpenRejectsWholeFlag 断言整份超标标记与逐项结论矛盾时整次 Open 失败：
// 返回 nil 存放、ErrCorruptRecord、信息点名样品编号并说明与逐项结论不一致，
// 原文件字节不变；不要求信息里出现某个项目名。
func assertOpenRejectsWholeFlag(t *testing.T, dir, sampleID string) {
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
		t.Fatalf("整份标记矛盾必须让整次 Open 失败，sample=%s", sampleID)
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
	if !strings.Contains(msg, "逐项") {
		t.Fatalf("错误信息应说明整份标记与逐项结论不一致，实际 %q", msg)
	}
	after, rerr := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if rerr != nil {
		t.Fatalf("read file after open: %v", rerr)
	}
	if string(after) != string(before) {
		t.Fatalf("打开失败不得覆盖或改写原数据文件")
	}
}

// 测量值 9、上限 8 的判定却保存为达标：即使项目、测量值都对得上，也不能把
// 与依据矛盾的超标标记当成可信结论，整次打开失败并点名样品与项目。
func TestOpenItemExceededMarkedCompliant(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				// 单项与整份都错成“达标”，单项矛盾必须先报出并点名项目。
				Status: StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: false},
				},
			},
		},
	})
	assertOpenRejects(t, dir, "S1", openPH, false)
}

// 测量值 7、上限 8 的达标判定却保存为超标：反向标错同样拒绝。
func TestOpenItemCompliantMarkedExceeded(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				// 单项错成超标；整份标记与错误的单项保持一致，
				// 证明只核对单项标记与保存依据这一条也足以拒绝。
				Status: StatusConfirmed, Exceeded: true,
				Results: []ItemResult{
					{Item: openPH, Value: 7, Limit: 8, LimitEffective: openEff(), Exceeded: true},
				},
			},
		},
	})
	assertOpenRejects(t, dir, "S1", openPH, false)
}

// 测量值恰好等于上限必须判为达标：保存成超标就是矛盾记录。
func TestOpenItemEqualLimitMarkedExceeded(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 8}},
				Status:       StatusConfirmed, Exceeded: true,
				Results: []ItemResult{
					{Item: openPH, Value: 8, Limit: 8, LimitEffective: openEff(), Exceeded: true},
				},
			},
		},
	})
	assertOpenRejects(t, dir, "S1", openPH, false)
}

// 零与负数的合法数值组合同样按“严格大于”判断。
func TestOpenItemZeroAndNegativeFlags(t *testing.T) {
	cases := []struct {
		name         string
		value        float64
		limit        float64
		savedFlag    bool
		wantExceed   bool
		shouldReject bool
	}{
		{"零等于零标成超标", 0, 0, true, false, true},
		{"零小于正上限标成超标", 0, 4, true, false, true},
		{"零大于负上限却标成达标", 0, -1, false, true, true},
		{"负数严格大于负上限却标成达标", -5, -10, false, true, true},
		{"负数等于负上限标成超标", -5, -5, true, false, true},
		{"负数小于负上限标成达标（正常）", -10, -5, false, false, false},
		{"零等于零标成达标（正常）", 0, 0, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeDiskFile(t, dir, diskState{
				Points: openPoints(),
				Samples: map[string]*Sample{
					"S1": {
						ID: "S1", PointID: "P1", SampledAt: at(10, 0),
						Measurements: []Measurement{{Item: openPH, Value: c.value}},
						// 整份标记与逐项实际结论一致，隔离出单项标记这一条核对。
						Status: StatusConfirmed, Exceeded: c.wantExceed,
						Results: []ItemResult{
							{Item: openPH, Value: c.value, Limit: c.limit, LimitEffective: openEff(), Exceeded: c.savedFlag},
						},
					},
				},
			})
			if c.shouldReject {
				assertOpenRejects(t, dir, "S1", openPH, false)
				return
			}
			s, err := Open(dir)
			if err != nil {
				t.Fatalf("标记与保存依据一致的合法数值组合应正常读入: %v", err)
			}
			defer s.Close()
			latest, ok, err := s.LatestResult("P1")
			if err != nil || !ok {
				t.Fatalf("正常记录应可作为最近有效结果: %+v ok=%v err=%v", latest, ok, err)
			}
			r := latest.Results[0]
			if r.Value != c.value || r.Limit != c.limit || r.Exceeded != c.wantExceed ||
				latest.Exceeded != c.wantExceed {
				t.Fatalf("保存的依据与标记应原样保留: %+v exceeded=%v", r, latest.Exceeded)
			}
		})
	}
}

// 逐项标记都正确（pH 9>8 超标、浊度 4=4 达标），整份标记却保存成达标：
// 单项核对放过、整份核对必须拦住，错误说明它与逐项结论不一致。
func TestOpenWholeFlagShouldBeExceeded(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: phTurbMeasurements(),
				Status:       StatusConfirmed, Exceeded: false,
				Results: phTurbResults(),
			},
		},
	})
	assertOpenRejectsWholeFlag(t, dir, "S1")
}

// 逐项全部达标，整份标记却保存成超标：反方向的整份标记矛盾同样拒绝。
func TestOpenWholeFlagShouldBeCompliant(t *testing.T) {
	dir := t.TempDir()
	results := []ItemResult{
		{Item: openPH, Value: 7, Limit: 8, LimitEffective: openEff(), Exceeded: false},
		{Item: openTurb, Value: 4, Limit: 4, LimitEffective: openEff(), Exceeded: false},
	}
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}, {Item: openTurb, Value: 4}},
				Status:       StatusConfirmed, Exceeded: true,
				Results: results,
			},
		},
	})
	assertOpenRejectsWholeFlag(t, dir, "S1")
}

// 已确认后作废、逐项依据仍保留的样品，超标标记与依据矛盾时同样拒绝：
// 作废只取消有效资格，不能让错误结论借“历史记录”名义混入台账。
func TestOpenVoidedAfterConfirmedItemFlagContradiction(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusVoided, VoidReason: "复测确认样品污染", Exceeded: false,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: false},
				},
			},
		},
	})
	assertOpenRejects(t, dir, "S1", openPH, false)
}

// 已确认后作废的样品单项都对、整份标记反了：同样整次拒绝并说明与逐项结论不一致。
func TestOpenVoidedAfterConfirmedWholeFlagContradiction(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusVoided, VoidReason: "复测确认样品污染", Exceeded: false,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
				},
			},
		},
	})
	assertOpenRejectsWholeFlag(t, dir, "S1")
}

// 一个文件里其他样品正常，也不能只跳过标记矛盾的样品继续打开。
func TestOpenFlagContradictionRejectsWholeFile(t *testing.T) {
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
			// 测量值 9、上限 8 却标成达标的损坏样品。
			"S-BAD": {
				ID: "S-BAD", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: false},
				},
			},
		},
	})
	assertOpenRejects(t, dir, "S-BAD", openPH, false)
}

// 核对只针对这份样品已保存的判定依据：保存的上限是 100、测量值 9 且标记达标，
// 即使文件里另有当前会选到的更严限值（8，超标），只要旧结论与旧依据一致就
// 照常打开，所用上限、生效时间和结论原样保留，不重新选择限值、不重做判定。
func TestOpenFlagCheckedAgainstSavedBasisOnly(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {
				{PointID: "P1", Item: openPH, Value: 8, Effective: at(1, 0)},
			},
		},
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					// 保存的旧依据是 9 <= 100 达标；按当前限值 8 重算会变成超标。
					{Item: openPH, Value: 9, Limit: 100, LimitEffective: at(2, 0), Exceeded: false},
				},
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("旧结论与旧依据一致时应正常读入，不能按当前限值重判: %v", err)
	}
	defer s.Close()
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("正常记录应可作为最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
	r := latest.Results[0]
	if r.Value != 9 || r.Limit != 100 || !r.LimitEffective.Equal(at(2, 0)) || r.Exceeded || latest.Exceeded {
		t.Fatalf("旧上限、生效时间与达标结论必须原样保留: %+v exceeded=%v", r, latest.Exceeded)
	}
}

// 保存依据是 9 <= 100 达标，却把单项标记写成超标：即使该标记恰好与当前更严
// 限值（8）会得出的结论相同，它仍与这份样品保存的依据矛盾，必须拒绝。
func TestOpenFlagContradictsSavedBasisEvenIfCurrentLimitAgrees(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {
				{PointID: "P1", Item: openPH, Value: 8, Effective: at(1, 0)},
			},
		},
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusConfirmed, Exceeded: true,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 100, LimitEffective: at(2, 0), Exceeded: true},
				},
			},
		},
	})
	assertOpenRejects(t, dir, "S1", openPH, false)
}

// 待判定样品没有判定依据，其整份超标标记零值 false 不参与核对，照常读入。
func TestOpenPendingWholeFlagNotChecked(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusPending,
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("待判定样品没有判定依据，不应核对超标标记: %v", err)
	}
	defer s.Close()
	if latest, ok, err := s.LatestResult("P1"); err != nil || ok {
		t.Fatalf("待判定样品不能成为最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
}

// 多项样品中只有一项标记相反：整次失败并点名出问题的项目，
// 且不改动原文件中的任何标记。
func TestOpenOneWrongItemFlagAmongMany(t *testing.T) {
	dir := t.TempDir()
	results := []ItemResult{
		{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
		// 浊度 4 <= 4 应为达标，却保存成超标；整份标记与逐项实际结论一致（true）。
		{Item: openTurb, Value: 4, Limit: 4, LimitEffective: openEff(), Exceeded: true},
	}
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: phTurbMeasurements(),
				Status:       StatusConfirmed, Exceeded: true,
				Results: results,
			},
		},
	})
	assertOpenRejects(t, dir, "S1", openTurb, false)
}

// assertOpenRejectsMissingLimit 断言判定缺少上限数值时整次 Open 失败：返回 nil
// 存放、ErrCorruptRecord、信息点名样品编号与项目并说明缺少上限，原文件字节不变。
func assertOpenRejectsMissingLimit(t *testing.T, dir, sampleID, item string) {
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
		t.Fatalf("判定缺少上限必须让整次 Open 失败，sample=%s", sampleID)
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
	if !strings.Contains(msg, item) {
		t.Fatalf("错误信息应指出项目 %q，实际 %q", item, msg)
	}
	if !strings.Contains(msg, "上限") {
		t.Fatalf("错误信息应说明判定缺少上限数值，实际 %q", msg)
	}
	after, rerr := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if rerr != nil {
		t.Fatalf("read file after open: %v", rerr)
	}
	if string(after) != string(before) {
		t.Fatalf("打开失败不得覆盖或改写原数据文件")
	}
}

// writeRawDiskFile 直接写入原始 JSON 数据文件，用于构造结构体序列化无法表达的
// 情况（如 limit 字段整个缺失或保存为 null）。
func writeRawDiskFile(t *testing.T, dir, raw string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "water-data.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write data file: %v", err)
	}
}

// 已确认样品的判定没有保存上限（limit 字段整个缺失）：即使原测量与判定测量值
// 都是零、单项与整份标记都是达标，内容互相对应，也不能把缺了判定依据的记录
// 当成“零等于零”的合法结论，整次打开失败并点名样品与项目。
func TestOpenConfirmedResultLimitFieldMissing(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 0}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "value": 0, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingLimit(t, dir, "S1", openPH)
}

// limit 字段保存为 JSON null 与字段缺失一样是判定依据残缺：读出的零值不能
// 被当成真实保存的上限，整次打开失败。
func TestOpenConfirmedResultLimitNull(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 0}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "value": 0, "limit": null, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingLimit(t, dir, "S1", openPH)
}

// 多项目样品只有一项缺少上限：不能只接收其他项目或保留整份结论，整次打开失败。
func TestOpenConfirmedOneResultLimitMissingAmongMany(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}, {"item": "浊度", "value": 4}],
      "status": "confirmed", "exceeded": false,
      "results": [
        {"item": "pH", "value": 7, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false},
        {"item": "浊度", "value": 4, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}
      ]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingLimit(t, dir, "S1", openTurb)
}

// 已确认后作废、逐项判定仍保留的样品，判定缺少上限同样不能借“历史记录”名义
// 混进台账：整次打开失败。
func TestOpenVoidedAfterConfirmedResultLimitMissing(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 9}],
      "status": "voided", "voidReason": "复测确认样品污染", "exceeded": true,
      "results": [{"item": "pH", "value": 9, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": true}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingLimit(t, dir, "S1", openPH)
}

// 明确保存数值零的上限是合法依据：测量值为零、上限为零、单项与整份都达标，
// 必须正常读入且上限原样保留为零，不能把零当成缺项。
func TestOpenConfirmedExplicitZeroLimitIsValid(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 0}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "value": 0, "limit": 0, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("明确保存的零上限是合法依据，应正常读入: %v", err)
	}
	defer s.Close()
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("零上限的完整记录应可作为最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
	r := latest.Results[0]
	if r.Value != 0 || r.Limit != 0 || r.Exceeded || latest.Exceeded {
		t.Fatalf("零上限与达标结论必须原样保留: %+v exceeded=%v", r, latest.Exceeded)
	}
}

// 同一文件中其他样品完整正常，也不能跳过缺上限的记录继续打开：整次失败、
// 无可用存放，正常样品同样读不到。
func TestOpenMissingLimitRejectsWholeFile(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S-GOOD": {
      "id": "S-GOOD", "pointId": "P1",
      "sampledAt": "` + at(5, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "value": 7, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    },
    "S-BAD": {
      "id": "S-BAD", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 0}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "value": 0, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingLimit(t, dir, "S-BAD", openPH)
}

// 即使文件里登记了采样当时适用的限值，也不能据此补造已保存结论缺少的依据：
// 缺上限的判定照样整次拒绝，不从登记的限值里找一版填上。
func TestOpenMissingLimitNotBackfilledFromRegisteredLimits(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"4:P1:2pH": [
    {"pointId": "P1", "item": "pH", "value": 8, "effective": "` +
		openEff().Format(time.RFC3339Nano) + `"}
  ]},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 9}],
      "status": "confirmed", "exceeded": true,
      "results": [{"item": "pH", "value": 9, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": true}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingLimit(t, dir, "S1", openPH)
}
