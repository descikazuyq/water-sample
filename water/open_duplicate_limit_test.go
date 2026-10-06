package water

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件保护“打开本地数据”时对限值版本唯一性的核对：
// 同一采样点、同一项目下，只要存在两条生效时刻相同的上限，整次 Open 就必须失败，
// 返回 nil 存放，错误可被 errors.Is(err, ErrDuplicateLimitTime) 识别，并点到具体
// 采样点、项目与重复的生效时间。即使两条数值完全相同也算重复：不合并、不择一、
// 不跳过问题组只读入其余部分；其他采样点与样品再正常也不部分读入；原文件不被
// 改写；拒绝发生在打开时，不推迟到确认某份样品。
//
// 归属只由每条记录自身的采样点编号与项目名共同确定，不看落盘键：重复版本放在
// 同一存储条目或分散在不同条目里都要识别；共用旧版存储键的不同（采样点，项目）
// 组合沿用现有兼容行为，不误判成同一组。生效时刻按真实时刻判断：不同时区偏移
// 表示同一瞬间仍算重复，相差一纳秒是两版合法版本。没有重复的多版本数据可乱序
// 保存后正常打开，待判定样品确认时继续采用采样当时已生效的最近一版，恰好等于
// 生效时刻采用新版；已保存的历史判定依据原样保留，不因读取检查重算。

// assertOpenRejectsDuplicateLimit 断言打开因限值生效时间重复而整次失败：
// 返回 nil 存放、错误为 ErrDuplicateLimitTime，信息点到采样点、项目与重复时刻，
// 原文件字节不变。
func assertOpenRejectsDuplicateLimit(t *testing.T, dir, point, item string, eff time.Time) {
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
		t.Fatalf("同组同刻的重复上限必须让整次 Open 失败，%s/%s @ %s", point, item, eff)
	}
	if got != nil {
		t.Fatalf("打开失败时不得返回可用的数据存放对象，got %#v", got)
	}
	if !errors.Is(err, ErrDuplicateLimitTime) {
		t.Fatalf("应返回 ErrDuplicateLimitTime，got %v", err)
	}
	msg := err.Error()
	// 与 SetLimit 拒绝重复登记时一致，时间用 Go time.String() 形式（含小数秒，
	// 保留纳秒精度），并统一到 UTC 表达。
	for _, want := range []string{point, item, eff.UTC().String()} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误信息应包含 %q（采样点、项目与重复生效时间），实际 %q", want, msg)
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

// 同一存储条目下，同一采样点同一项目的两条上限生效时刻相同，即使数值不同，
// 也必须在打开时整次拒绝；文件中另有正常采样点与待判定样品也不能部分读入。
func TestOpenDuplicateLimitSameEntryRejectsWholeFile(t *testing.T) {
	dir := t.TempDir()
	eff := at(5, 0)
	writeDiskFile(t, dir, diskState{
		Points: map[string]SamplingPoint{
			"P1": {ID: "P1", Name: "一号取水口"},
			"P2": {ID: "P2", Name: "二号取水口"},
		},
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {
				{PointID: "P1", Item: openPH, Value: 7, Effective: eff},
				{PointID: "P1", Item: openPH, Value: 9, Effective: eff},
			},
			// 另一采样点的正常限值不能“稀释”问题组。
			diskLimitKey("P2", openPH): {
				{PointID: "P2", Item: openPH, Value: 8, Effective: at(1, 0)},
			},
		},
		Samples: map[string]*Sample{
			// 与重复限值无关的正常待判定样品，也不能被部分读入。
			"S-OK": {
				ID: "S-OK", PointID: "P2", SampledAt: at(6, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusPending,
			},
		},
	})
	assertOpenRejectsDuplicateLimit(t, dir, "P1", openPH, eff)
}

// 两条上限连数值都完全相同，仍是重复版本：不能合并、不能当成同一条放行。
func TestOpenDuplicateLimitIdenticalRecords(t *testing.T) {
	dir := t.TempDir()
	eff := at(5, 0)
	dup := Limit{PointID: "P1", Item: openPH, Value: 7, Effective: eff}
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {dup, dup},
		},
	})
	assertOpenRejectsDuplicateLimit(t, dir, "P1", openPH, eff)
}

// 同一生效时刻的两条版本分散在不同存储条目下，只看存储键就发现不了：
// 读取按每条记录自身的采样点与项目重新分组，仍必须识别为同一组重复。
func TestOpenDuplicateLimitSpreadAcrossStorageKeys(t *testing.T) {
	dir := t.TempDir()
	eff := at(5, 0)
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			"任意键-甲": {
				{PointID: "P1", Item: openPH, Value: 7, Effective: eff},
			},
			"任意键-乙": {
				{PointID: "P1", Item: openPH, Value: 9, Effective: eff},
			},
		},
	})
	assertOpenRejectsDuplicateLimit(t, dir, "P1", openPH, eff)
}

// 不同时区偏移但表示同一瞬间的两个日期仍算重复：不能按时区字面值放行。
func TestOpenDuplicateLimitSameInstantDifferentZones(t *testing.T) {
	dir := t.TempDir()
	utc := time.Date(2026, 9, 5, 0, 0, 0, 500_000_000, time.UTC)
	beijing := time.Date(2026, 9, 5, 8, 0, 0, 500_000_000, time.FixedZone("CST", 8*3600))
	if !utc.Equal(beijing) {
		t.Fatal("测试前提：两种时区写法应表示同一真实时刻")
	}
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {
				{PointID: "P1", Item: openPH, Value: 7, Effective: utc},
				{PointID: "P1", Item: openPH, Value: 9, Effective: beijing},
			},
		},
	})
	assertOpenRejectsDuplicateLimit(t, dir, "P1", openPH, utc)
}

// 相差一纳秒的两版生效时间是合法的不同版本，不能按秒截断后误报：
// 文件正常打开，边界前一纳秒用旧版、恰好生效用新版。
func TestOpenLimitsOneNanosecondApartAreDistinctVersions(t *testing.T) {
	dir := t.TempDir()
	boundary := time.Date(2026, 9, 10, 12, 0, 0, 500_000_000, time.UTC)
	oldEff := boundary.Add(-1) // 与新版落在同一秒内，仅差一纳秒
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {
				// 故意乱序排列，证明排序后按纳秒精度比较。
				{PointID: "P1", Item: openPH, Value: 5, Effective: boundary},
				{PointID: "P1", Item: openPH, Value: 10, Effective: oldEff},
			},
		},
		Samples: map[string]*Sample{
			"S-BEFORE": {
				ID: "S-BEFORE", PointID: "P1", SampledAt: oldEff,
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusPending,
			},
			"S-AT": {
				ID: "S-AT", PointID: "P1", SampledAt: boundary,
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusPending,
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("相差一纳秒的两版应正常打开: %v", err)
	}
	defer s.Close()

	before, err := s.Confirm("S-BEFORE")
	if err != nil {
		t.Fatalf("Confirm S-BEFORE: %v", err)
	}
	if r := before.Results[0]; r.Limit != 10 || !r.LimitEffective.Equal(oldEff) || r.Exceeded {
		t.Fatalf("边界前一纳秒应采用旧版 10 且 7 ≤ 10 达标: %+v", r)
	}
	at, err := s.Confirm("S-AT")
	if err != nil {
		t.Fatalf("Confirm S-AT: %v", err)
	}
	if r := at.Results[0]; r.Limit != 5 || !r.LimitEffective.Equal(boundary) || !r.Exceeded {
		t.Fatalf("恰好生效应采用新版 5 且 7 > 5 超标: %+v", r)
	}
}

// 不同采样点或不同项目在同一时刻各登记一版限值完全合法：文件正常打开，
// 确认时各组只用自己的限值。
func TestOpenSameEffectiveTimeAcrossDifferentGroupsIsLegal(t *testing.T) {
	dir := t.TempDir()
	eff := at(1, 0)
	writeDiskFile(t, dir, diskState{
		Points: map[string]SamplingPoint{
			"P1": {ID: "P1", Name: "一号取水口"},
			"P2": {ID: "P2", Name: "二号取水口"},
		},
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {
				{PointID: "P1", Item: openPH, Value: 8, Effective: eff},
			},
			diskLimitKey("P2", openPH): {
				{PointID: "P2", Item: openPH, Value: 6, Effective: eff},
			},
			diskLimitKey("P1", "COD"): {
				{PointID: "P1", Item: "COD", Value: 30, Effective: eff},
			},
		},
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(2, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusPending,
			},
			"S2": {
				ID: "S2", PointID: "P2", SampledAt: at(2, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusPending,
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("不同组在同一时刻登记限值应正常打开: %v", err)
	}
	defer s.Close()
	r1, err := s.Confirm("S1")
	if err != nil || r1.Results[0].Limit != 8 || r1.Exceeded {
		t.Fatalf("P1/pH 应用本组的 8、7 ≤ 8 达标: %+v err=%v", r1, err)
	}
	r2, err := s.Confirm("S2")
	if err != nil || r2.Results[0].Limit != 6 || !r2.Exceeded {
		t.Fatalf("P2/pH 应用本组的 6、7 > 6 超标: %+v err=%v", r2, err)
	}
}

// 编号与项目名内部的 U+0000 继续支持：旧版本单字符拼接键会把两个不同组
// 放进同一个存储条目，同一时刻各有一版是合法的；但其中一组在同一条目下
// 再出现同刻第二版，仍必须按记录自身的归属识别为重复并整次拒绝。
func TestOpenDuplicateLimitWithinOldCollidedKey(t *testing.T) {
	dir := t.TempDir()
	zeroPoint := "P\x00A"
	zeroItem := "A\x00B"
	eff := at(1, 0)
	// 与 limit_group_test.go 中旧格式相同的碰撞键：两组共用。
	oldKey := zeroPoint + "\x00" + "B"
	if oldKey != "P"+"\x00"+zeroItem {
		t.Fatal("测试前提：两个组合应拼出同一个旧键")
	}

	// 先验证只有同刻各一版时沿用兼容行为、正常打开。
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
		t.Fatalf("共用旧键的两个不同组同刻各一版应正常读入: %v", err)
	}
	if got := s.limits[limitGroup{pointID: "P", item: zeroItem}]; len(got) != 1 {
		t.Fatalf("旧键重新分组错误: %+v", got)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 同一碰撞条目里给 "P"/zeroItem 再放一条同刻版本：必须识别为该组重复。
	writeDiskFile(t, dir, diskState{
		Points: map[string]SamplingPoint{
			zeroPoint: {ID: zeroPoint, Name: "带零编号"},
			"P":       {ID: "P", Name: "普通编号"},
		},
		Limits: map[string][]Limit{
			oldKey: {
				{PointID: zeroPoint, Item: "B", Value: 10, Effective: eff},
				{PointID: "P", Item: zeroItem, Value: 5, Effective: eff},
				{PointID: "P", Item: zeroItem, Value: 6, Effective: eff},
			},
		},
	})
	assertOpenRejectsDuplicateLimit(t, dir, "P", zeroItem, eff)
}

// 没有重复的多版本数据即使在文件中乱序保存，也照常打开并按生效时刻排序：
// 采样于各时刻之间的待判定样品取采样当时已生效的最近一版，边界时刻用新版；
// 已确认样品保存的历史判定依据原样保留，不按读入的限值重算。
func TestOpenUnorderedMultiVersionLimitsLoadAndConfirm(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {
				// 故意按 10 日、1 日、5 日的乱序排列落盘。
				{PointID: "P1", Item: openPH, Value: 6, Effective: at(10, 0)},
				{PointID: "P1", Item: openPH, Value: 10, Effective: at(1, 0)},
				{PointID: "P1", Item: openPH, Value: 7, Effective: at(5, 0)},
			},
		},
		Samples: map[string]*Sample{
			// 待判定：9/6 采样，应取 9/5 生效的 7，7 ≤ 7 达标。
			"SP": {
				ID: "SP", PointID: "P1", SampledAt: at(6, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusPending,
			},
			// 待判定：恰好 9/10 采样，采用新版 6，7 > 6 超标。
			"SAT": {
				ID: "SAT", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusPending,
			},
			// 已确认样品保存的是旧依据 10@9/1、判超标（9 > 10 为假，这里保存
			// 11 > 10 超标），即使与当前限值重算结果不同也必须原样保留。
			"SC": {
				ID: "SC", PointID: "P1", SampledAt: at(2, 0),
				Measurements: []Measurement{{Item: openPH, Value: 11}},
				Status:       StatusConfirmed, Exceeded: true,
				Results: []ItemResult{{
					Item: openPH, Value: 11, Limit: 10,
					LimitEffective: at(1, 0), Exceeded: true,
				}},
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("无重复的乱序多版本数据应正常打开: %v", err)
	}
	defer s.Close()

	sp, err := s.Confirm("SP")
	if err != nil {
		t.Fatalf("Confirm SP: %v", err)
	}
	if r := sp.Results[0]; r.Limit != 7 || !r.LimitEffective.Equal(at(5, 0)) || r.Exceeded {
		t.Fatalf("9/6 采样应取 9/5 生效的 7、等于上限达标: %+v", r)
	}
	sat, err := s.Confirm("SAT")
	if err != nil {
		t.Fatalf("Confirm SAT: %v", err)
	}
	if r := sat.Results[0]; r.Limit != 6 || !r.LimitEffective.Equal(at(10, 0)) || !r.Exceeded {
		t.Fatalf("恰好生效时刻应采用新版 6 并超标: %+v", r)
	}
	// 历史结论原样保留：重复确认直接返回已保存依据，不重算。
	sc, err := s.Confirm("SC")
	if err != nil {
		t.Fatalf("Confirm SC: %v", err)
	}
	if r := sc.Results[0]; r.Limit != 10 || !r.LimitEffective.Equal(at(1, 0)) ||
		!r.Exceeded || !sc.Exceeded {
		t.Fatalf("已确认样品的历史判定依据必须原样保留: %+v", r)
	}
}

// 文件里同时存在重复限值组和已确认样品时，仍然整次失败：
// 不能先读入样品、把拒绝推迟到确认时，也不重算任何历史依据。
func TestOpenDuplicateLimitRejectsEvenWithConfirmedSample(t *testing.T) {
	dir := t.TempDir()
	eff := at(1, 0)
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {
				{PointID: "P1", Item: openPH, Value: 8, Effective: eff},
				{PointID: "P1", Item: openPH, Value: 9, Effective: eff},
			},
		},
		Samples: map[string]*Sample{
			"SC": {
				ID: "SC", PointID: "P1", SampledAt: at(2, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusConfirmed, Exceeded: true,
				Results: []ItemResult{{
					Item: openPH, Value: 9, Limit: 8,
					LimitEffective: eff, Exceeded: true,
				}},
			},
		},
	})
	assertOpenRejectsDuplicateLimit(t, dir, "P1", openPH, eff)
}

// 打开因重复限值失败后不留下任何内部状态：把问题组修成唯一版本覆盖写回，
// 同一目录即可正常打开，历史样品照常可见。
func TestOpenDuplicateLimitRejectionLeavesNoState(t *testing.T) {
	dir := t.TempDir()
	eff := at(1, 0)
	bad := diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {
				{PointID: "P1", Item: openPH, Value: 8, Effective: eff},
				{PointID: "P1", Item: openPH, Value: 9, Effective: eff},
			},
		},
		Samples: map[string]*Sample{
			"SP": {
				ID: "SP", PointID: "P1", SampledAt: at(2, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusPending,
			},
		},
	}
	writeDiskFile(t, dir, bad)
	if got, err := Open(dir); !errors.Is(err, ErrDuplicateLimitTime) || got != nil {
		t.Fatalf("重复限值必须拒绝，got=%v err=%v", got, err)
	}
	good := bad
	good.Limits[diskLimitKey("P1", openPH)] = []Limit{
		{PointID: "P1", Item: openPH, Value: 8, Effective: eff},
	}
	writeDiskFile(t, dir, good)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("修正为唯一版本后应正常打开: %v", err)
	}
	defer s.Close()
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 || list[0].ID != "SP" {
		t.Fatalf("修正后应能看到原有样品: %+v err=%v", list, err)
	}
}
