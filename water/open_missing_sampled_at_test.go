package water

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件保护“打开本地数据”时对样品采样时间的完整性核对：每份样品都必须保留
// 采样时间。sampledAt 字段缺失、保存为 null 或明确保存为 Go 零时间
// 0001-01-01T00:00:00Z，JSON 反序列化后都是零时间，一律视为缺少采样时间。
// 这条要求适用于待判定、已确认和已作废的全部样品：作废前是否确认过、是否仍有
// 逐项判定都不影响；测量项目和数值完整，或者保留了相互一致的达标、超标依据，
// 也不能代替日期。只要文件里有一份样品缺少采样时间，整次 Open 就失败、返回
// nil 存放、错误可识别为 ErrCorruptRecord，信息点到具体样品编号并说明缺少的是
// 样品的采样时间（与登记限值或判定依据缺少生效时间相区分）；同一文件中其他样品
// 正常也不只读入正常部分，拒绝时原文件字节不变，不补日期、不猜日期、不改变样品
// 状态或已有结论。已确认记录缺日期时必须明确报告日期缺失，不能只说某项上限晚于
// 采样时间。完整日期按真实时刻继续使用，空数据照常打开。

// assertOpenRejectsMissingSampledAt 断言打开因某份样品缺少采样时间而整次失败：
// 返回 nil 存放、ErrCorruptRecord，信息点名样品编号并说明缺少样品的采样时间
// （带出 sampledAt 字段名），且不能退化成“上限生效时间晚于采样时间”的说法；
// 原文件字节不被改动。
func assertOpenRejectsMissingSampledAt(t *testing.T, dir, sampleID string) {
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
		t.Fatalf("缺少采样时间的样品必须让整次 Open 失败，sample=%s", sampleID)
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
	for _, want := range []string{"采样时间", "sampledAt"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误信息应明确说明缺少样品的采样时间（含 %q），实际 %q", want, msg)
		}
	}
	// 哨兵错误自身的说明里带有“晚于采样时间”字样，因此只检查去掉哨兵前缀后的
	// 具体原因：采样时间本身缺失时必须明确报日期缺失，不能退化成某项上限晚于
	// 采样时间的说法。
	detail := strings.TrimPrefix(msg, ErrCorruptRecord.Error())
	if strings.Contains(detail, "晚于采样时间") {
		t.Fatalf("采样时间本身缺失时必须明确报告日期缺失，不能只说上限晚于采样时间：%q", msg)
	}
	after, rerr := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if rerr != nil {
		t.Fatalf("read file after open: %v", rerr)
	}
	if string(after) != string(before) {
		t.Fatalf("打开失败不得覆盖或改写原数据文件")
	}
}

// 三种缺日期写法（字段缺失、null、Go 零时间）对待判定样品都必须整次拒绝。
// 直接写原始 JSON 覆盖字段缺失与 null 两种 Go 结构体序列化表达不出的形态。
func TestOpenPendingMissingSampledAtForms(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{
			"字段缺失",
			`{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {"id": "S1", "pointId": "P1",
      "measurements": [{"item": "pH", "value": 7}],
      "status": "pending"}
  }
}`,
		},
		{
			"保存为null",
			`{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {"id": "S1", "pointId": "P1", "sampledAt": null,
      "measurements": [{"item": "pH", "value": 7}],
      "status": "pending"}
  }
}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeRawDiskFile(t, dir, c.raw)
			assertOpenRejectsMissingSampledAt(t, dir, "S1")
		})
	}

	// 明确保存为 Go 零时间：用结构体序列化得到 0001-01-01T00:00:00Z。
	t.Run("Go零时间", func(t *testing.T) {
		dir := t.TempDir()
		writeDiskFile(t, dir, diskState{
			Points: openPoints(),
			Samples: map[string]*Sample{
				"S1": {
					ID: "S1", PointID: "P1", SampledAt: time.Time{},
					Measurements: []Measurement{{Item: openPH, Value: 7}},
					Status:       StatusPending,
				},
			},
		})
		assertOpenRejectsMissingSampledAt(t, dir, "S1")
	})
}

// 已确认样品缺采样时间：即使测量项目、数值完整且保存了逐项判定与整份超标标记，
// 这些内容也不能代替日期；错误必须明确说缺少采样时间，而不是退化成判定依据的
// 上限生效时间“晚于采样时间”（零采样时间会让任何生效时间都显得晚于它）。
func TestOpenConfirmedMissingSampledAt(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{
			"字段缺失但依据完整自洽",
			`{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {"id": "S1", "pointId": "P1",
      "measurements": [{"item": "pH", "value": 9}],
      "status": "confirmed", "exceeded": true,
      "results": [{"item": "pH", "value": 9, "limit": 8,
        "limitEffective": "` + openEff().Format(time.RFC3339Nano) + `", "exceeded": true}]}
  }
}`,
		},
		{
			"保存为null且依据完整自洽",
			`{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {"id": "S1", "pointId": "P1", "sampledAt": null,
      "measurements": [{"item": "pH", "value": 7}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "value": 7, "limit": 8,
        "limitEffective": "` + openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]}
  }
}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeRawDiskFile(t, dir, c.raw)
			assertOpenRejectsMissingSampledAt(t, dir, "S1")
		})
	}

	// Go 零时间：达标依据自洽也不能放行。
	t.Run("Go零时间且依据完整自洽", func(t *testing.T) {
		dir := t.TempDir()
		writeDiskFile(t, dir, diskState{
			Points: openPoints(),
			Samples: map[string]*Sample{
				"S1": {
					ID: "S1", PointID: "P1", SampledAt: time.Time{},
					Measurements: []Measurement{{Item: openPH, Value: 7}},
					Status:       StatusConfirmed, Exceeded: false,
					Results: []ItemResult{
						{Item: openPH, Value: 7, Limit: 8, LimitEffective: openEff(), Exceeded: false},
					},
				},
			},
		})
		assertOpenRejectsMissingSampledAt(t, dir, "S1")
	})
}

// 待判定后直接作废（没有逐项判定）的样品缺采样时间：作废不免除日期要求，
// 未确认就作废也不需要补判定依据，缺日期本身就足以让整次打开失败。
func TestOpenVoidedFromPendingMissingSampledAt(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: time.Time{},
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusVoided, VoidReason: "录入信息有误",
			},
		},
	})
	assertOpenRejectsMissingSampledAt(t, dir, "S1")
}

// 已确认后作废、逐项依据仍完整保留的样品缺采样时间：历史结论自洽也不能代替
// 日期，整次打开失败并明确报告缺少采样时间。
func TestOpenVoidedAfterConfirmedMissingSampledAt(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: time.Time{},
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusVoided, VoidReason: "复测确认样品污染", Exceeded: true,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
				},
			},
		},
	})
	assertOpenRejectsMissingSampledAt(t, dir, "S1")
}

// 测量项目与数值完整也不能代替采样时间：多项目、零值合法组合的待判定样品缺
// 日期时仍整次拒绝。
func TestOpenMissingSampledAtCompleteMeasurementsNotEnough(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: time.Time{},
				Measurements: []Measurement{{Item: openPH, Value: 0}, {Item: openTurb, Value: 0}},
				Status:       StatusPending,
			},
		},
	})
	assertOpenRejectsMissingSampledAt(t, dir, "S1")
}

// 同一文件中其他样品正常，也不能只读入正常部分：一份缺日期即整次失败，
// 完整样品也读不到，原文件保持原样。
func TestOpenMissingSampledAtRejectsWholeFile(t *testing.T) {
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
			// 缺采样时间的待判定样品。
			"S-BAD": {
				ID: "S-BAD", PointID: "P1", SampledAt: time.Time{},
				Measurements: []Measurement{{Item: openPH, Value: 6}},
				Status:       StatusPending,
			},
		},
	})
	assertOpenRejectsMissingSampledAt(t, dir, "S-BAD")
}

// 多份样品缺日期时按编号稳定报出其中一份（编号升序），且仍然整次失败。
func TestOpenMultipleMissingSampledAtSortedReport(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S-Z": {
				ID: "S-Z", PointID: "P1", SampledAt: time.Time{},
				Measurements: []Measurement{{Item: openPH, Value: 6}},
				Status:       StatusPending,
			},
			"S-A": {
				ID: "S-A", PointID: "P1", SampledAt: time.Time{},
				Measurements: []Measurement{{Item: openPH, Value: 6}},
				Status:       StatusPending,
			},
		},
	})
	assertOpenRejectsMissingSampledAt(t, dir, "S-A")
}

// 正常流程录入的样品本就带采样时间：关闭后重新打开必须原样保留真实时刻，
// 按点台账排列与最近有效结果不受影响，已确认依据也不被重判。
func TestOpenCompleteSamplesRoundTrip(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "一号取水口")
	mustLimit(t, s, "P1", openPH, 8.0, openEff())
	mustSample(t, s, "S1", "P1", at(10, 0), Measurement{Item: openPH, Value: 9})
	conf, err := s.Confirm("S1")
	if err != nil || !conf.Exceeded {
		t.Fatalf("setup: Confirm = %+v, err=%v", conf, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("日期完整的记录重新打开不应失败: %v", err)
	}
	defer s2.Close()
	list, err := s2.ListByPoint("P1")
	if err != nil || len(list) != 1 || !list[0].SampledAt.Equal(at(10, 0)) {
		t.Fatalf("采样真实时刻必须原样保留: %+v err=%v", list, err)
	}
	latest, ok, err := s2.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" || !latest.Exceeded {
		t.Fatalf("最近有效结果与历史结论必须保留: %+v ok=%v err=%v", latest, ok, err)
	}
	if r := latest.Results[0]; r.Limit != 8 || !r.LimitEffective.Equal(openEff()) {
		t.Fatalf("已保存的上限与生效时间必须原样保留: %+v", r)
	}
}
