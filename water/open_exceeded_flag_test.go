package water

import (
	"strings"
	"testing"
)

// 本文件保护“打开本地数据”时对超标标记的核对：
// 对已确认样品、以及已确认后作废仍保存逐项判定依据的样品，每条判定的超标
// 标记必须等于“该条保存的测量值严格大于保存的上限”，小于或等于（含零、负数）
// 都应为达标；整份样品的超标标记还必须与逐项结论一致，任一超标即为真、全部
// 达标即为假。单项标记相反、或单项都对但整份标记相反，都必须让整次 Open 失败，
// 返回 nil 存放、ErrCorruptRecord，信息点到样品编号（单项错误还点到项目，
// 整份标记错误说明与逐项结论不一致），且原文件字节不变。核对只依据已保存的
// 判定内容，不按后来新增或补录的限值重算。

// 测量值 9、上限 8 却标为达标：单项超标标记与保存的依据矛盾，整次打开失败。
func TestOpenItemExceededFalseWhenValueAboveLimit(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: phTurbMeasurements(),
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openTurb, Value: 4, Limit: 4, LimitEffective: openEff(), Exceeded: false},
					// pH 9 > 8 却保存为达标，整份标记也被一起写成假。
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: false},
				},
			},
		},
	})
	assertOpenRejects(t, dir, "S1", openPH, false)
}

// 测量值恰好等于上限不属于超标：保存为超标同样是矛盾记录，整次打开失败。
func TestOpenItemExceededTrueWhenValueEqualsLimit(t *testing.T) {
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

// 单项都正确但整份标记相反：有项目超标却标为整份达标，必须拒绝，
// 错误信息说明整份标记与逐项结论不一致（不要求点到具体项目）。
func TestOpenSampleFlagFalseWhileItemExceeded(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: phTurbMeasurements(),
				Status:       StatusConfirmed, Exceeded: false,
				Results: phTurbResults(), // 浊度达标、pH 超标：整份应为真
			},
		},
	})
	assertOpenRejectsWholeFlag(t, dir, "S1")
}

// 单项都正确但整份标记相反：全部达标却标为整份超标，同样必须拒绝。
func TestOpenSampleFlagTrueWhileAllItemsPass(t *testing.T) {
	results := []ItemResult{
		{Item: openTurb, Value: 4, Limit: 4, LimitEffective: openEff(), Exceeded: false},
		{Item: openPH, Value: 7, Limit: 8, LimitEffective: openEff(), Exceeded: false},
	}
	dir := t.TempDir()
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

// assertOpenRejectsWholeFlag 断言整份超标标记矛盾时整次 Open 失败，
// 且错误信息点名样品并说明整份标记与逐项结论不一致。
func assertOpenRejectsWholeFlag(t *testing.T, dir, sampleID string) {
	t.Helper()
	assertOpenRejects(t, dir, sampleID, "", true)
	got, err := Open(dir)
	if err == nil {
		t.Fatalf("损坏数据必须让整次 Open 失败")
	}
	if got != nil {
		t.Fatalf("打开失败时不得返回可用的数据存放对象，got %#v", got)
	}
	if !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("错误信息应说明整份标记与逐项结论不一致，实际 %q", err.Error())
	}
}

// 零和负数都是合法数值，严格大于规则照样适用：
// 等于、小于上限的保存为达标必须正常读入；严格大于却保存为达标的必须拒绝。
func TestOpenExceededFlagWithZeroAndNegativeValues(t *testing.T) {
	cases := []struct {
		name     string
		value    float64
		limit    float64
		exceeded bool
		wantBad  bool
	}{
		{"零等于零_达标", 0, 0, false, false},
		{"负数小于零_达标", -5, 0, false, false},
		{"负数等于负数_达标", -1, -1, false, false},
		{"零大于负上限却标达标", 0, -1, false, true},
		{"负上限下负测量相等却标超标", -1, -1, true, true},
		{"负数小于上限却标超标", -9, -8, true, true},
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
						Status:       StatusConfirmed, Exceeded: c.exceeded,
						Results: []ItemResult{
							{Item: openPH, Value: c.value, Limit: c.limit, LimitEffective: openEff(), Exceeded: c.exceeded},
						},
					},
				},
			})
			if c.wantBad {
				assertOpenRejects(t, dir, "S1", openPH, false)
				return
			}
			s, err := Open(dir)
			if err != nil {
				t.Fatalf("标记与保存依据一致的记录应正常读入: %v", err)
			}
			defer s.Close()
			latest, ok, err := s.LatestResult("P1")
			if err != nil || !ok || latest.ID != "S1" {
				t.Fatalf("正常记录应能作为最近有效结果: %+v ok=%v err=%v", latest, ok, err)
			}
			r := latest.Results[0]
			if r.Value != c.value || r.Limit != c.limit || r.Exceeded != c.exceeded ||
				latest.Exceeded != c.exceeded {
				t.Fatalf("保存的依据与标记必须原样保留: %+v sample.exceeded=%v", r, latest.Exceeded)
			}
		})
	}
}

// 已确认后作废、仍保存逐项判定依据的样品，单项标记矛盾也不能借“历史记录”
// 名义混入：整次打开失败并点名样品与项目。
func TestOpenVoidedAfterConfirmedBadItemFlag(t *testing.T) {
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

// 已确认后作废的样品单项都对、整份标记相反，同样必须拒绝。
func TestOpenVoidedAfterConfirmedBadWholeFlag(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: phTurbMeasurements(),
				Status:       StatusVoided, VoidReason: "复测确认样品污染", Exceeded: false,
				Results: phTurbResults(), // pH 超标，整份应为真
			},
		},
	})
	assertOpenRejectsWholeFlag(t, dir, "S1")
}

// 一个文件里其他样品正常，也不能只跳过标记矛盾的样品：整次失败、无可用存放，
// 最近有效结果也不能返回错误结论。
func TestOpenBadExceededFlagRejectsWholeFile(t *testing.T) {
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
			// 测量值 9、上限 8 却标为达标的损坏样品。
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

// 核对只依据已保存的判定内容：旧结论与旧依据一致（8@生效日、9 超标）时，
// 即使文件里同时存在一版会让重算结果变成达标的限值，也必须原样打开，
// 保存的上限、生效时间、超标结论与整份标记都不变。
func TestOpenExceededFlagNotRecomputedFromCurrentLimits(t *testing.T) {
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

	// 补录采样之前就生效的宽松限值：若按当前限值重算，9 会变成达标。
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen for backfill: %v", err)
	}
	mustLimit(t, s2, "P1", openPH, 100.0, at(2, 0))
	if err := s2.Close(); err != nil {
		t.Fatalf("Close s2: %v", err)
	}

	s3, err := Open(dir)
	if err != nil {
		t.Fatalf("旧结论与旧依据一致时不应因新限值被拒绝: %v", err)
	}
	defer s3.Close()
	latest, ok, err := s3.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
	r := latest.Results[0]
	if r.Value != 9 || r.Limit != 8.0 || !r.LimitEffective.Equal(openEff()) || !r.Exceeded || !latest.Exceeded {
		t.Fatalf("已保存依据与结论必须原样保留，不能按当前限值重算: %+v sample.exceeded=%v",
			r, latest.Exceeded)
	}
	// 重复确认仍返回保存的旧结论。
	again, err := s3.Confirm("S1")
	if err != nil || !again.Exceeded || again.Results[0].Limit != 8.0 {
		t.Fatalf("重复确认应返回旧依据: %+v err=%v", again, err)
	}
}

// 待判定、待判定后直接作废的样品没有判定依据，超标标记核对不适用，继续读入；
// 已有测试已覆盖其读取路径，这里确认空 Exceeded 字段不会被误判为“整份标记相反”。
func TestOpenSamplesWithoutResultsHaveNoFlagCheck(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"SP": {
				ID: "SP", PointID: "P1", SampledAt: at(2, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusPending,
			},
			"SV": {
				ID: "SV", PointID: "P1", SampledAt: at(3, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusVoided, VoidReason: "录入信息有误",
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("没有判定依据的样品不应参与超标标记核对: %v", err)
	}
	defer s.Close()
	if list, err := s.ListByPoint("P1"); err != nil || len(list) != 2 {
		t.Fatalf("两份样品都应正常读入: %+v err=%v", list, err)
	}
}
