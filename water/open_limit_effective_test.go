package water

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件保护“打开本地数据”时对已保存判定依据的时间核对：
// 带结论的样品（已确认，以及已确认后作废、逐项依据仍保留在 Results 中的样品），
// 每条逐项判定保存的上限生效时间必须存在且不晚于这份样品的采样时间——确认时只
// 选用采样当时已经生效的上限，保存下来的依据也必须遵守同一规则。limitEffective
// 字段缺失、保存为 null 或落盘为 Go 零时间都表示缺少生效依据，不能当成一版很早
// 的上限放行；生效时间晚于采样时间（按真实时刻比较，保留纳秒精度：恰好相等可以
// 接受，晚一纳秒也拒绝；不同时区写法表示同一时刻按相等处理）也必须整次拒绝，
// 即使测量值、上限数值与超标标记互相对得上。拒绝返回 nil 存放、错误可识别为
// ErrCorruptRecord 并点名样品编号与项目，区分“缺少生效时间”与“晚于采样时间”
// 两种信息，后者同时带出两处时间且保留足以说明先后的小数秒；原文件不变，不跳过
// 记录、不补填日期、不替换上限、不退回待判定、不重新计算结论。核对只针对样品自己
// 保存的依据：后来补录的采样之前生效的新上限不影响旧结论原样读入；没有逐项判定
// 的待判定样品与直接作废的样品照常打开。

// assertOpenRejectsEffective 在 assertOpenRejects 的基础上进一步断言错误信息
// 包含（或不包含）特定片段，用于区分“缺少生效时间”与“晚于采样时间”两种拒绝。
func assertOpenRejectsEffective(t *testing.T, dir, sampleID, item string, contains, notContains []string) {
	t.Helper()
	assertOpenRejects(t, dir, sampleID, item, false)
	// assertOpenRejects 已经重新打开过一次并断言失败；这里再开一次取错误信息。
	// 它同时保证了原文件未被改动，因此再次打开得到的错误与第一次一致。
	_, err := Open(dir)
	if err == nil {
		t.Fatalf("损坏数据必须让整次 Open 失败，sample=%s", sampleID)
	}
	// 只核对哨兵文本之后的具体原因部分：ErrCorruptRecord 的文案枚举了所有
	// 损坏类型，两种生效时间问题的区分体现在各条错误的具体描述里。
	msg := strings.TrimPrefix(err.Error(), ErrCorruptRecord.Error())
	for _, want := range contains {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误信息应包含 %q，实际 %q", want, msg)
		}
	}
	for _, unwanted := range notContains {
		if strings.Contains(msg, unwanted) {
			t.Fatalf("错误信息不应包含 %q，实际 %q", unwanted, msg)
		}
	}
}

// 任务示例：样品 9 月 10 日采样，保存的浊度上限却标明 9 月 11 日生效，即使测量值
// 4、上限 5 与达标标记互相对得上，也必须在打开时拒绝；错误点名样品与项目，说明
// 生效时间晚于采样时间并带出两处时间。
func TestOpenConfirmedLimitEffectiveAfterSampling(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openTurb, Value: 4}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openTurb, Value: 4, Limit: 5, LimitEffective: at(11, 0), Exceeded: false},
				},
			},
		},
	})
	assertOpenRejectsEffective(t, dir, "S1", openTurb,
		[]string{"晚于采样时间",
			at(11, 0).Format(time.RFC3339Nano), at(10, 0).Format(time.RFC3339Nano)},
		[]string{"缺少上限生效时间"})
}

// 生效时间比采样时间晚一纳秒也必须拒绝：先后按真实时刻判断，保留纳秒精度；
// 错误信息中的两处时间必须带足以说明先后的小数秒。
func TestOpenLimitEffectiveOneNanosecondLate(t *testing.T) {
	dir := t.TempDir()
	sampled := time.Date(2026, 9, 10, 12, 0, 0, 500_000_000, time.UTC)
	effective := sampled.Add(1) // 晚一纳秒
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: sampled,
				Measurements: []Measurement{{Item: openTurb, Value: 4}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openTurb, Value: 4, Limit: 5, LimitEffective: effective, Exceeded: false},
				},
			},
		},
	})
	assertOpenRejectsEffective(t, dir, "S1", openTurb,
		[]string{"晚于采样时间", "2026-09-10T12:00:00.500000001Z", "2026-09-10T12:00:00.5Z"},
		nil)
}

// 生效时间恰好等于采样时间可以接受：确认时“恰好生效”即选用该版，保存的依据同理。
func TestOpenLimitEffectiveExactlyAtSampling(t *testing.T) {
	dir := t.TempDir()
	sampled := time.Date(2026, 9, 10, 12, 0, 0, 500_000_000, time.UTC)
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: sampled,
				Measurements: []Measurement{{Item: openTurb, Value: 4}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openTurb, Value: 4, Limit: 5, LimitEffective: sampled, Exceeded: false},
				},
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("生效时间恰好等于采样时间应正常读入: %v", err)
	}
	defer s.Close()
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("最近有效结果应指向 S1: %+v ok=%v err=%v", latest, ok, err)
	}
	r := latest.Results[0]
	if r.Limit != 5 || !r.LimitEffective.Equal(sampled) || r.Exceeded {
		t.Fatalf("判定依据应原样保留: %+v", r)
	}
}

// 不同时区的写法若代表同一时刻，按相等处理：生效时间用 +08:00 写法、采样时间用
// UTC 写法表示同一真实时刻（含小数秒），必须正常读入。
func TestOpenLimitEffectiveSameInstantDifferentZone(t *testing.T) {
	dir := t.TempDir()
	sampled := time.Date(2026, 9, 10, 12, 0, 0, 500_000_000, time.UTC)
	beijing := time.FixedZone("CST", 8*3600)
	effective := time.Date(2026, 9, 10, 20, 0, 0, 500_000_000, beijing)
	if !effective.Equal(sampled) {
		t.Fatal("测试前提：两种时区写法应表示同一真实时刻")
	}
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: sampled,
				Measurements: []Measurement{{Item: openTurb, Value: 4}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openTurb, Value: 4, Limit: 5, LimitEffective: effective, Exceeded: false},
				},
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("同一时刻的不同时区写法应按相等处理、正常读入: %v", err)
	}
	defer s.Close()
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || !latest.Results[0].LimitEffective.Equal(sampled) {
		t.Fatalf("生效依据应原样保留: %+v ok=%v err=%v", latest, ok, err)
	}
}

// limitEffective 字段缺失、保存为 null 或落盘为 Go 零时间，都表示缺少生效依据，
// 不能当成一版很早的上限放行；三种写法共用“缺少上限生效时间”的拒绝信息，
// 与“晚于采样时间”相区分。直接写原始 JSON，因为 Go 结构体序列化总会带上该字段。
func TestOpenConfirmedLimitEffectiveMissing(t *testing.T) {
	cases := []struct {
		name           string
		effectiveField string // 拼进判定记录的 limitEffective 字段文本
	}{
		{"字段缺失", ""},
		{"保存为null", `"limitEffective": null,`},
		{"Go零时间", `"limitEffective": "0001-01-01T00:00:00Z",`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "浊度", "value": 4}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "浊度", "value": 4, "limit": 5, ` + c.effectiveField + ` "exceeded": false}]
    }
  }
}`
			if err := os.WriteFile(filepath.Join(dir, "water-data.json"), []byte(raw), 0o644); err != nil {
				t.Fatalf("write data file: %v", err)
			}
			assertOpenRejectsEffective(t, dir, "S1", openTurb,
				[]string{"缺少上限生效时间"},
				[]string{"晚于采样时间"})
		})
	}
}

// 已确认后作废、逐项依据仍保留的样品同样要过时间核对：作废只取消有效资格，
// 不能让采样之后才生效的上限借“历史记录”名义混入台账。
func TestOpenVoidedAfterConfirmedLimitEffectiveAfterSampling(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openTurb, Value: 4}},
				Status:       StatusVoided, VoidReason: "复测确认样品污染", Exceeded: false,
				Results: []ItemResult{
					{Item: openTurb, Value: 4, Limit: 5, LimitEffective: at(11, 0), Exceeded: false},
				},
			},
		},
	})
	assertOpenRejectsEffective(t, dir, "S1", openTurb,
		[]string{"晚于采样时间"}, []string{"缺少上限生效时间"})
}

// 多项目样品中只要一项的生效时间不符合，整次打开失败并点名该项目；
// 同文件中有其他正常样品也不能继续打开。
func TestOpenOneBadEffectiveTimeAmongManyRejectsWholeFile(t *testing.T) {
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
			// 双项目样品：pH 依据合法，浊度的上限生效时间晚于采样时间。
			"S-BAD": {
				ID: "S-BAD", PointID: "P1", SampledAt: at(10, 0),
				Measurements: phTurbMeasurements(),
				Status:       StatusConfirmed, Exceeded: true,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
					{Item: openTurb, Value: 4, Limit: 5, LimitEffective: at(11, 0), Exceeded: false},
				},
			},
		},
	})
	assertOpenRejectsEffective(t, dir, "S-BAD", openTurb,
		[]string{"晚于采样时间"}, nil)
}

// 核对只检查样品自己保存的生效依据，不要求它仍是当前登记版本中最新的一版：
// 后来补录了采样之前生效的新上限（若重算会改变结论），只要旧依据的生效时间
// 合法且其余完整性核对通过，旧结论仍原样读入。
func TestOpenOldBasisAcceptedDespiteBackfilledLimit(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			// 后来补录的、采样之前生效的更严上限：重算会把 4 判成超标。
			diskLimitKey("P1", openTurb): {
				{PointID: "P1", Item: openTurb, Value: 3, Effective: at(2, 0)},
			},
		},
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openTurb, Value: 4}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					// 保存的旧依据：上限 5、采样前生效、4 <= 5 达标，自身完全合法。
					{Item: openTurb, Value: 4, Limit: 5, LimitEffective: openEff(), Exceeded: false},
				},
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("旧依据生效时间合法时应原样读入，不按补录限值重算: %v", err)
	}
	defer s.Close()
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("最近有效结果应指向 S1: %+v ok=%v err=%v", latest, ok, err)
	}
	r := latest.Results[0]
	if r.Value != 4 || r.Limit != 5 || !r.LimitEffective.Equal(openEff()) || r.Exceeded || latest.Exceeded {
		t.Fatalf("旧上限、生效时间与达标结论必须原样保留: %+v exceeded=%v", r, latest.Exceeded)
	}
}

// 没有逐项判定的待判定样品及直接作废的样品照常打开，不为它们补造上限或结论；
// 已作废样品继续仅供历史核对，不成为最近有效结果。
func TestOpenPendingAndDirectVoidedNotCheckedForEffectiveTime(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"SP": {
				ID: "SP", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openTurb, Value: 4}},
				Status:       StatusPending,
			},
			"SV": {
				ID: "SV", PointID: "P1", SampledAt: at(11, 0),
				Measurements: []Measurement{{Item: openTurb, Value: 4}},
				Status:       StatusVoided, VoidReason: "录入信息有误",
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("无逐项判定的待判定与直接作废样品应照常打开: %v", err)
	}
	defer s.Close()
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	for _, smp := range list {
		if len(smp.Results) != 0 || smp.Exceeded {
			t.Fatalf("不应为 %s 补造上限或结论: %+v", smp.ID, smp)
		}
	}
	if latest, ok, err := s.LatestResult("P1"); err != nil || ok {
		t.Fatalf("待判定与直接作废样品不应成为最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
}

// 打开因生效时间核对失败时不得改动原文件（assertOpenRejects 已逐字节核对），
// 这里再确认失败之后用合法依据覆盖重写可以正常打开，证明拒绝只发生在读取核对，
// 没有留下任何内部状态。
func TestOpenEffectiveTimeRejectionLeavesNoState(t *testing.T) {
	dir := t.TempDir()
	bad := diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openTurb, Value: 4}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openTurb, Value: 4, Limit: 5, LimitEffective: at(11, 0), Exceeded: false},
				},
			},
		},
	}
	writeDiskFile(t, dir, bad)
	if got, err := Open(dir); !errors.Is(err, ErrCorruptRecord) || got != nil {
		t.Fatalf("生效时间晚于采样时间必须拒绝, got=%v err=%v", got, err)
	}
	good := bad
	good.Samples["S1"].Results[0].LimitEffective = openEff()
	writeDiskFile(t, dir, good)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("修正为合法依据后应正常打开: %v", err)
	}
	defer s.Close()
}
