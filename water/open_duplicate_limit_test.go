package water

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件保护“打开本地数据”时对限值生效时刻唯一性的核对：
// 同一采样点、同一项目下只要存在两条生效时刻相同的上限，整次 Open 必须失败，
// 返回 nil 存放，错误可被 errors.Is(err, ErrDuplicateLimitTime) 识别，并点到
// 具体采样点、项目与重复的生效时间。即使两条数值完全相同也算重复，不能合并、
// 择一或跳过问题组；文件里其他采样点和样品正常，也不能只读入正常部分。
// 生效时刻按真实瞬间比较：不同时区偏移表示同一时刻算重复，只差一纳秒的两个
// 生效时间是各自合法的不同版本，不能按秒截断误报。限值归属由每条记录自身的
// pointId 与 item 共同确定，重复版本无论放在同一存储条目还是分散在不同条目下
// 都必须被识别；不同采样点或不同项目在同一时刻登记限值仍合法，共用一个旧存储
// 键的不同组合也不能误判成同一组。拒绝只发生在读取阶段：原文件字节不变，
// 不推迟到确认样品时。没有重复的多版本数据乱序保存后照常打开，确认时仍取
// 采样当时已生效的最近一版，恰好在生效时刻采用新值；已保存的历史判定依据
// 原样保留。

// assertOpenRejectsDuplicateLimit 断言打开因重复生效时刻整次失败：
// nil 存放、ErrDuplicateLimitTime，信息点名采样点、项目与重复时间，原文件不变。
func assertOpenRejectsDuplicateLimit(t *testing.T, dir, pointID, item string, eff time.Time) {
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
		t.Fatalf("重复生效时刻的限值必须让整次 Open 失败，point=%s item=%s", pointID, item)
	}
	if got != nil {
		t.Fatalf("打开失败时不得返回可用的数据存放对象，got %#v", got)
	}
	if !errors.Is(err, ErrDuplicateLimitTime) {
		t.Fatalf("应返回 ErrDuplicateLimitTime，got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{
		pointID, item,
		eff.UTC().Format(time.RFC3339Nano),
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误信息应包含 %q，实际 %q", want, msg)
		}
	}
	after, rerr := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if rerr != nil {
		t.Fatalf("read file after open: %v", rerr)
	}
	if string(after) != string(before) {
		t.Fatalf("打开失败不得覆盖或改写原数据文件")
	}
}

// 同一存储条目下，同一采样点同一项目的两条上限生效时间完全相同，
// 即使数值完全一样也必须整次拒绝；错误点名采样点、项目与生效时间。
func TestOpenDuplicateLimitSameValueSameEntry(t *testing.T) {
	dir := t.TempDir()
	eff := at(10, 0)
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openTurb): {
				{PointID: "P1", Item: openTurb, Value: 5, Effective: eff},
				{PointID: "P1", Item: openTurb, Value: 5, Effective: eff},
			},
		},
	})
	assertOpenRejectsDuplicateLimit(t, dir, "P1", openTurb, eff)
}

// 两条同刻版本数值不同、在列表中不相邻（中间隔着另一时刻的版本）也必须识别：
// 读取排序后它们相邻，判定依据不唯一，整次失败。
func TestOpenDuplicateLimitDifferentValueNonAdjacent(t *testing.T) {
	dir := t.TempDir()
	eff := at(10, 0)
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openTurb): {
				{PointID: "P1", Item: openTurb, Value: 5, Effective: eff},
				{PointID: "P1", Item: openTurb, Value: 4, Effective: at(1, 0)},
				{PointID: "P1", Item: openTurb, Value: 6, Effective: eff},
			},
		},
	})
	assertOpenRejectsDuplicateLimit(t, dir, "P1", openTurb, eff)
}

// 重复版本分散在不同存储条目下（手工构造的文件里同一组挂在两个键下），
// 读取时按每条记录自身的采样点与项目归组，仍必须识别重复并整次失败。
func TestOpenDuplicateLimitAcrossStorageEntries(t *testing.T) {
	dir := t.TempDir()
	eff := at(10, 0)
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openTurb): {
				{PointID: "P1", Item: openTurb, Value: 5, Effective: eff},
			},
			// 落盘键与归属无关：这个键下也挂着一条同组同刻版本。
			"some-other-key": {
				{PointID: "P1", Item: openTurb, Value: 6, Effective: eff},
			},
		},
	})
	assertOpenRejectsDuplicateLimit(t, dir, "P1", openTurb, eff)
}

// 不同时区偏移表示同一真实瞬间的两个生效时间算重复，不能按字面年月日比较；
// 错误信息中的重复时间取该真实瞬间。
func TestOpenDuplicateLimitSameInstantDifferentZone(t *testing.T) {
	dir := t.TempDir()
	effUTC := time.Date(2026, 9, 10, 12, 0, 0, 500_000_000, time.UTC)
	beijing := time.FixedZone("CST", 8*3600)
	effBJ := time.Date(2026, 9, 10, 20, 0, 0, 500_000_000, beijing)
	if !effBJ.Equal(effUTC) {
		t.Fatal("测试前提：两种时区写法应表示同一真实时刻")
	}
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openTurb): {
				{PointID: "P1", Item: openTurb, Value: 5, Effective: effUTC},
				{PointID: "P1", Item: openTurb, Value: 6, Effective: effBJ},
			},
		},
	})
	assertOpenRejectsDuplicateLimit(t, dir, "P1", openTurb, effUTC)
}

// 只差一纳秒的两个生效时间是合法的不同版本：必须正常打开，不能按秒截断误报。
func TestOpenLimitsOneNanosecondApartAreDistinctVersions(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 9, 10, 12, 0, 0, 500_000_000, time.UTC)
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openTurb): {
				// 故意乱序保存，且两个时刻 Unix 秒数相同。
				{PointID: "P1", Item: openTurb, Value: 5, Effective: base.Add(1)},
				{PointID: "P1", Item: openTurb, Value: 10, Effective: base},
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("相差一纳秒的两版限值应正常打开: %v", err)
	}
	defer s.Close()
	versions := s.limits[limitGroup{pointID: "P1", item: openTurb}]
	if len(versions) != 2 || !versions[0].Effective.Before(versions[1].Effective) {
		t.Fatalf("读入后应按生效时间升序排列: %+v", versions)
	}
	if versions[0].Value != 10 || versions[1].Value != 5 {
		t.Fatalf("读入后版本内容或顺序错误: %+v", versions)
	}

	// 前一纳秒采样用旧版 10（7 达标），恰好生效与后一纳秒用新版 5（7 超标）。
	mustSample(t, s, "S-BEFORE", "P1", base, Measurement{Item: openTurb, Value: 7})
	r0, err := s.Confirm("S-BEFORE")
	if err != nil || r0.Results[0].Limit != 10 || r0.Exceeded {
		t.Fatalf("前一纳秒应用旧版 10 且达标: %+v err=%v", r0, err)
	}
	mustSample(t, s, "S-AT", "P1", base.Add(1), Measurement{Item: openTurb, Value: 7})
	r1, err := s.Confirm("S-AT")
	if err != nil || r1.Results[0].Limit != 5 || !r1.Exceeded ||
		!r1.Results[0].LimitEffective.Equal(base.Add(1)) {
		t.Fatalf("恰好生效时刻应采用新版 5 且超标: %+v err=%v", r1, err)
	}
}

// 不同采样点在同一时刻各自登记限值是合法的，不能误报重复。
func TestOpenSameEffectiveTimeDifferentPointsIsLegal(t *testing.T) {
	dir := t.TempDir()
	eff := at(10, 0)
	writeDiskFile(t, dir, diskState{
		Points: map[string]SamplingPoint{
			"P1": {ID: "P1", Name: "一号取水口"},
			"P2": {ID: "P2", Name: "二号取水口"},
		},
		Limits: map[string][]Limit{
			diskLimitKey("P1", openTurb): {
				{PointID: "P1", Item: openTurb, Value: 5, Effective: eff},
			},
			diskLimitKey("P2", openTurb): {
				{PointID: "P2", Item: openTurb, Value: 8, Effective: eff},
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("不同采样点同一时刻登记限值应合法: %v", err)
	}
	defer s.Close()
	mustSample(t, s, "S1", "P1", at(11, 0), Measurement{Item: openTurb, Value: 7})
	r1, err := s.Confirm("S1")
	if err != nil || r1.Results[0].Limit != 5 || !r1.Exceeded {
		t.Fatalf("P1 应采用本组的 5: %+v err=%v", r1, err)
	}
	mustSample(t, s, "S2", "P2", at(11, 0), Measurement{Item: openTurb, Value: 7})
	r2, err := s.Confirm("S2")
	if err != nil || r2.Results[0].Limit != 8 || r2.Exceeded {
		t.Fatalf("P2 应采用本组的 8: %+v err=%v", r2, err)
	}
}

// 同一采样点的不同项目在同一时刻各自登记限值是合法的，不能误报重复。
func TestOpenSameEffectiveTimeDifferentItemsIsLegal(t *testing.T) {
	dir := t.TempDir()
	eff := at(10, 0)
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {
				{PointID: "P1", Item: openPH, Value: 8, Effective: eff},
			},
			diskLimitKey("P1", openTurb): {
				{PointID: "P1", Item: openTurb, Value: 5, Effective: eff},
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("同一采样点不同项目同一时刻登记限值应合法: %v", err)
	}
	defer s.Close()
	mustSample(t, s, "S1", "P1", at(11, 0),
		Measurement{Item: openPH, Value: 9}, Measurement{Item: openTurb, Value: 4})
	r, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	rs := rsByItem(r.Results)
	if rs[openPH].Limit != 8 || !rs[openPH].Exceeded {
		t.Fatalf("pH 应采用本组 8 且超标: %+v", rs[openPH])
	}
	if rs[openTurb].Limit != 5 || rs[openTurb].Exceeded {
		t.Fatalf("浊度应采用本组 5 且达标: %+v", rs[openTurb])
	}
}

// 编号与项目名内部的 U+0000 继续支持：两组各自的同刻限值互不冲突，
// 也不能因单字符拼接的归属判断把它们误并成一组；而同一组内真有两条
// 同刻版本时仍要识别。
func TestOpenDuplicateLimitWithU0000Ownership(t *testing.T) {
	dir := t.TempDir()
	zeroPoint := "P\x00A"
	zeroItem := "A\x00B"
	eff := at(10, 0)

	// 先验证两组共用同一旧存储键、同一时刻各有一版限值时仍正常打开。
	oldKey := zeroPoint + "\x00" + "B"
	if oldKey != "P"+"\x00"+zeroItem {
		t.Fatal("test setup: 两个组合应拼出同一个旧键")
	}
	writeDiskFile(t, dir, diskState{
		Points: map[string]SamplingPoint{
			zeroPoint: {ID: zeroPoint, Name: "带零编号"},
			"P":       {ID: "P", Name: "普通编号"},
		},
		Limits: map[string][]Limit{
			oldKey: {
				{PointID: zeroPoint, Item: "B", Value: 10, Effective: eff},
				{PointID: "P", Item: zeroItem, Value: 5, Effective: eff},
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("共用旧存储键的不同组合同刻限值不应误报重复: %v", err)
	}
	if got := s.limits[limitGroup{pointID: zeroPoint, item: "B"}]; len(got) != 1 {
		t.Fatalf("带零编号组归属错误: %+v", got)
	}
	if got := s.limits[limitGroup{pointID: "P", item: zeroItem}]; len(got) != 1 {
		t.Fatalf("普通编号组归属错误: %+v", got)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 再把其中一组改成真的同刻两版：即使它们与另一组共用旧键，
	// 重复仍按记录自身的归属识别，整次失败并点名该组。
	writeDiskFile(t, dir, diskState{
		Points: map[string]SamplingPoint{
			zeroPoint: {ID: zeroPoint, Name: "带零编号"},
			"P":       {ID: "P", Name: "普通编号"},
		},
		Limits: map[string][]Limit{
			oldKey: {
				{PointID: zeroPoint, Item: "B", Value: 10, Effective: eff},
				{PointID: "P", Item: zeroItem, Value: 5, Effective: eff},
				{PointID: zeroPoint, Item: "B", Value: 11, Effective: eff},
			},
		},
	})
	assertOpenRejectsDuplicateLimit(t, dir, zeroPoint, "B", eff)
}

// 一个限值组存在同刻重复时，即使文件里其他采样点、限值与样品都正常，
// 整次打开仍失败、无可用存放，不能只读入正常部分。
func TestOpenDuplicateLimitRejectsWholeFile(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: map[string]SamplingPoint{
			"P1": {ID: "P1", Name: "一号取水口"},
			"P2": {ID: "P2", Name: "二号取水口"},
		},
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {
				{PointID: "P1", Item: openPH, Value: 8, Effective: at(1, 0)},
			},
			diskLimitKey("P2", openPH): {
				{PointID: "P2", Item: openPH, Value: 9, Effective: at(1, 0)},
				// P2/pH 同刻两版，是本文件唯一的问题组。
				{PointID: "P2", Item: openPH, Value: 7, Effective: at(1, 0)},
			},
		},
		Samples: map[string]*Sample{
			"S-GOOD": {
				ID: "S-GOOD", PointID: "P1", SampledAt: at(5, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openPH, Value: 7, Limit: 8, LimitEffective: at(1, 0), Exceeded: false},
				},
			},
		},
	})
	assertOpenRejectsDuplicateLimit(t, dir, "P2", openPH, at(1, 0))

	// 修复文件（删掉重复版本）后应能正常打开，正常样品与限值原样可用，
	// 证明拒绝只发生在读取核对、没有改写原文件或留下内部状态。
	writeDiskFile(t, dir, diskState{
		Points: map[string]SamplingPoint{
			"P1": {ID: "P1", Name: "一号取水口"},
			"P2": {ID: "P2", Name: "二号取水口"},
		},
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {
				{PointID: "P1", Item: openPH, Value: 8, Effective: at(1, 0)},
			},
			diskLimitKey("P2", openPH): {
				{PointID: "P2", Item: openPH, Value: 9, Effective: at(1, 0)},
			},
		},
		Samples: map[string]*Sample{
			"S-GOOD": {
				ID: "S-GOOD", PointID: "P1", SampledAt: at(5, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openPH, Value: 7, Limit: 8, LimitEffective: at(1, 0), Exceeded: false},
				},
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("去掉重复版本后应正常打开: %v", err)
	}
	defer s.Close()
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S-GOOD" {
		t.Fatalf("正常样品应原样读入: %+v ok=%v err=%v", latest, ok, err)
	}
}

// 没有重复的多版本限值乱序保存后正常打开：读入后按生效时间升序，
// 确认样品继续取采样当时已生效的最近一版，恰好在生效时刻采用新值；
// 已确认样品保存的历史判定依据原样保留，不按限值重算。
func TestOpenUnsortedDistinctVersionsWorkAsBefore(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {
				// 乱序：9/20、9/1、9/10。
				{PointID: "P1", Item: openPH, Value: 30, Effective: at(20, 0)},
				{PointID: "P1", Item: openPH, Value: 10, Effective: at(1, 0)},
				{PointID: "P1", Item: openPH, Value: 20, Effective: at(10, 0)},
			},
		},
		Samples: map[string]*Sample{
			// 已确认样品保存的是当时的旧依据 10@9/1，即使后来补了更严的版本也原样保留。
			"S-OLD": {
				ID: "S-OLD", PointID: "P1", SampledAt: at(5, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 10, LimitEffective: at(1, 0), Exceeded: false},
				},
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("无重复的乱序多版本应正常打开: %v", err)
	}
	defer s.Close()

	versions := s.limits[limitGroup{pointID: "P1", item: openPH}]
	if len(versions) != 3 {
		t.Fatalf("三版限值都应读入: %+v", versions)
	}
	for i := 1; i < len(versions); i++ {
		if !versions[i-1].Effective.Before(versions[i].Effective) {
			t.Fatalf("读入后应按生效时间升序排列: %+v", versions)
		}
	}

	// 历史判定依据原样保留（9 <= 10 达标），不按当前限值重算。
	old, err := s.Confirm("S-OLD")
	if err != nil || old.Results[0].Limit != 10 || old.Exceeded {
		t.Fatalf("已确认样品的历史依据应原样保留: %+v err=%v", old, err)
	}

	// 待判定样品按采样时刻选版：9/5 采样用 10；恰好 9/10 生效时刻用新版 20；
	// 9/15 采样仍用 20。
	mustSample(t, s, "S1", "P1", at(5, 12), Measurement{Item: openPH, Value: 15})
	r1, err := s.Confirm("S1")
	if err != nil || r1.Results[0].Limit != 10 || !r1.Exceeded {
		t.Fatalf("9/5 采样应取 10@9/1 且超标: %+v err=%v", r1, err)
	}
	mustSample(t, s, "S2", "P1", at(10, 0), Measurement{Item: openPH, Value: 20})
	r2, err := s.Confirm("S2")
	if err != nil || r2.Results[0].Limit != 20 || r2.Exceeded ||
		!r2.Results[0].LimitEffective.Equal(at(10, 0)) {
		t.Fatalf("恰好生效时刻应采用新版 20 且等于上限达标: %+v err=%v", r2, err)
	}
	mustSample(t, s, "S3", "P1", at(15, 0), Measurement{Item: openPH, Value: 25})
	r3, err := s.Confirm("S3")
	if err != nil || r3.Results[0].Limit != 20 || !r3.Exceeded {
		t.Fatalf("9/15 采样应取最近已生效的 20 且超标: %+v err=%v", r3, err)
	}
}

// 读取阶段拒绝重复后，正常登记路径的同组同刻第二版拒绝行为保持不变：
// 打开一个含合法多版本的文件后，SetLimit 再提交同刻版本仍被拒绝且不覆盖原值。
func TestSetLimitDuplicateStillRejectedAfterOpen(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {
				{PointID: "P1", Item: openPH, Value: 10, Effective: at(1, 0)},
				{PointID: "P1", Item: openPH, Value: 20, Effective: at(10, 0)},
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	same := time.Date(2026, 9, 10, 8, 0, 0, 0, time.FixedZone("CST", 8*3600)) // == at(10,0) UTC
	if _, err := s.SetLimit("P1", openPH, 99, same); !errors.Is(err, ErrDuplicateLimitTime) {
		t.Fatalf("同组同一时刻换时区表示的第二版应被拒绝, got %v", err)
	}
	mustSample(t, s, "S1", "P1", at(11, 0), Measurement{Item: openPH, Value: 25})
	r, err := s.Confirm("S1")
	if err != nil || r.Results[0].Limit != 20 || !r.Exceeded {
		t.Fatalf("被拒绝的登记不得覆盖原值: %+v err=%v", r, err)
	}
}
