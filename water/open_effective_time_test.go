package water

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件保护“打开本地数据”时对逐项判定上限生效时间的时间核对：
// 已确认样品，以及已确认后作废、逐项判定仍保留的样品，每条判定保存的
// limitEffective 都必须实际存在（字段缺失、null 或 Go 零时间都算缺少生效
// 依据），且该生效时刻不得晚于样品采样时刻——采样当时该版上限必须已经生效。
// 即使测量值、上限数值与超标标记三者互相对得上（如测量值 4、上限 5、达标），
// 只要生效时间晚于采样时间，整次 Open 仍必须失败、返回 nil 存放、错误可识别
// 为 ErrCorruptRecord 并点名样品编号与项目；不补填日期、不替换上限、不退回
// 待判定、不重新判定、不改写原文件。时间先后按真实时刻并保留纳秒精度：
// 恰好相等接受，晚一纳秒拒绝；不同时区写法表示同一时刻时按相等处理。
// 多项目样品只要一项不符整次打开失败，同文件其他正常样品也读不到；
// 待判定样品与待判定后直接作废、没有逐项判定的样品不适用本核对，照常打开。

// openTimeBoundary 是一组带小数秒的真实时刻，用于纳秒与时区边界用例。
func openTimeBoundary() time.Time {
	return time.Date(2026, 9, 10, 12, 0, 0, 500_000_000, time.UTC) // 12:00:00.5
}

// assertOpenRejectsEffective 断言打开因上限生效时间问题整次失败，并核对错误
// 信息区分“生效时间缺失”与“生效时间晚于采样时间”两类原因。
func assertOpenRejectsEffective(t *testing.T, dir, sampleID, itemHint, reasonHint string, times ...time.Time) {
	t.Helper()
	before, err := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if err != nil {
		t.Fatalf("read file before open: %v", err)
	}
	got, oerr := Open(dir)
	if oerr == nil {
		if got != nil {
			got.Close()
		}
		t.Fatalf("生效依据不合法必须让整次 Open 失败，sample=%s", sampleID)
	}
	if got != nil {
		t.Fatalf("打开失败时不得返回可用的数据存放对象，got %#v", got)
	}
	if !errors.Is(oerr, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，got %v", oerr)
	}
	msg := oerr.Error()
	if !strings.Contains(msg, sampleID) {
		t.Fatalf("错误信息应指出样品编号 %q，实际 %q", sampleID, msg)
	}
	if itemHint != "" && !strings.Contains(msg, itemHint) {
		t.Fatalf("错误信息应指出项目 %q，实际 %q", itemHint, msg)
	}
	if !strings.Contains(msg, reasonHint) {
		t.Fatalf("错误信息应区分原因 %q，实际 %q", reasonHint, msg)
	}
	for _, tm := range times {
		want := tm.UTC().Format(time.RFC3339Nano)
		if !strings.Contains(msg, want) {
			t.Fatalf("错误信息应带出真实时刻 %q（保留小数秒），实际 %q", want, msg)
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

// 题目主场景：样品 9 月 10 日采样，保存的浊度上限却标明 9 月 11 日生效。
// 测量值 4、上限 5、单项与整份标记均为达标，三者互相对得上，但该版上限
// 采样次日才生效，整次打开仍必须拒绝并点名样品与浊度项目。
func TestOpenConfirmedEffectiveAfterSampling(t *testing.T) {
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
	assertOpenRejectsEffective(t, dir, "S1", openTurb, "晚于采样时间", at(11, 0), at(10, 0))
}

// 生效时间晚于采样时间时，即使文件里登记着采样当时适用的其他版本限值，
// 也不能替换成另一版上限或重新判定：保存的结论仍整次拒绝。
func TestOpenFutureEffectiveNotReplacedByRegisteredLimit(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openTurb): {
				{PointID: "P1", Item: openTurb, Value: 3, Effective: at(1, 0)},
			},
		},
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openTurb, Value: 4}},
				// 保存的依据是 4 ≤ 5 达标，但 5 这版 9/11 才生效；
				// 已登记的 3@9/1 采样当时已生效（4 > 3 本应超标）也不能借用。
				Status: StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openTurb, Value: 4, Limit: 5, LimitEffective: at(11, 0), Exceeded: false},
				},
			},
		},
	})
	assertOpenRejectsEffective(t, dir, "S1", openTurb, "晚于采样时间")
}

// limitEffective 字段整个缺失：上限数值、测量值与达标标记都对得上，
// 仍因缺少生效依据拒绝，错误与“晚于采样时间”区分开。
func TestOpenConfirmedMissingEffectiveField(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "浊度", "value": 4}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "浊度", "value": 4, "limit": 5, "exceeded": false}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsEffective(t, dir, "S1", openTurb, "缺少上限生效时间")
}

// limitEffective 保存为 null：与字段缺失同为缺少生效依据。
func TestOpenConfirmedNullEffective(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "浊度", "value": 4}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "浊度", "value": 4, "limit": 5, "limitEffective": null, "exceeded": false}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsEffective(t, dir, "S1", openTurb, "缺少上限生效时间")
}

// limitEffective 显式保存成 Go 零时间（0001-01-01T00:00:00Z）：字段虽然
// 存在，但零时间表示缺少一版真实的生效时刻，不能当成“很早以前就生效”放行。
func TestOpenConfirmedZeroEffective(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openTurb, Value: 4}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openTurb, Value: 4, Limit: 5, LimitEffective: time.Time{}, Exceeded: false},
				},
			},
		},
	})
	assertOpenRejectsEffective(t, dir, "S1", openTurb, "缺少上限生效时间")
}

// 生效时间恰好等于采样时间：可以接受，读入后可作为最近有效结果。
func TestOpenEffectiveExactlyAtSamplingAccepted(t *testing.T) {
	dir := t.TempDir()
	boundary := openTimeBoundary()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: boundary,
				Measurements: []Measurement{{Item: openTurb, Value: 4}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openTurb, Value: 4, Limit: 5, LimitEffective: boundary, Exceeded: false},
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
		t.Fatalf("完整记录应可作为最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
	r := latest.Results[0]
	if r.Limit != 5 || !r.LimitEffective.Equal(boundary) || r.Exceeded {
		t.Fatalf("恰好生效的依据必须原样保留: %+v", r)
	}
}

// 生效时间比采样时间早一纳秒：已经生效，正常读入。
func TestOpenEffectiveOneNanosecondBeforeAccepted(t *testing.T) {
	dir := t.TempDir()
	boundary := openTimeBoundary()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: boundary,
				Measurements: []Measurement{{Item: openTurb, Value: 4}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openTurb, Value: 4, Limit: 5, LimitEffective: boundary.Add(-1), Exceeded: false},
				},
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("生效时间早一纳秒应正常读入: %v", err)
	}
	defer s.Close()
}

// 生效时间比采样时间晚一纳秒：同一秒内也必须区分，整次拒绝，错误信息带出
// 两处带小数秒的真实时刻，足以看出先后。
func TestOpenEffectiveOneNanosecondAfterRejected(t *testing.T) {
	dir := t.TempDir()
	boundary := openTimeBoundary()
	future := boundary.Add(1)
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: boundary,
				Measurements: []Measurement{{Item: openTurb, Value: 4}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openTurb, Value: 4, Limit: 5, LimitEffective: future, Exceeded: false},
				},
			},
		},
	})
	assertOpenRejectsEffective(t, dir, "S1", openTurb, "晚于采样时间", future, boundary)
}

// 采样时间与生效时间用不同时区写法表示同一真实时刻时，必须按同一时刻接受；
// 晚一纳秒的版本即便换时区写法仍要拒绝。这里直接写 JSON 控制两边的字面量。
func TestOpenEffectiveAcrossTimeZones(t *testing.T) {
	boundary := openTimeBoundary() // 2026-09-10T12:00:00.5Z
	// 同一真实时刻的北京时间写法：2026-09-10T20:00:00.5+08:00。
	sameInstantBeijing := "2026-09-10T20:00:00.5+08:00"
	// 比采样时刻晚一纳秒的北京时间写法。
	futureBeijing := "2026-09-10T20:00:00.500000001+08:00"

	t.Run("同时刻不同时区写法接受", func(t *testing.T) {
		dir := t.TempDir()
		raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + boundary.Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "浊度", "value": 4}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "浊度", "value": 4, "limit": 5, "limitEffective": "` +
			sameInstantBeijing + `", "exceeded": false}]
    }
  }
}`
		writeRawDiskFile(t, dir, raw)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("不同时区写法表示同一真实时刻应按相等接受: %v", err)
		}
		defer s.Close()
		latest, ok, err := s.LatestResult("P1")
		if err != nil || !ok {
			t.Fatalf("正常记录应可作为最近有效结果: %+v ok=%v err=%v", latest, ok, err)
		}
		if !latest.Results[0].LimitEffective.Equal(boundary) {
			t.Fatalf("保存的生效依据应与采样时刻是同一真实时刻: %s", latest.Results[0].LimitEffective)
		}
	})

	t.Run("晚一纳秒换时区写法仍拒绝", func(t *testing.T) {
		dir := t.TempDir()
		raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + boundary.Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "浊度", "value": 4}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "浊度", "value": 4, "limit": 5, "limitEffective": "` +
			futureBeijing + `", "exceeded": false}]
    }
  }
}`
		writeRawDiskFile(t, dir, raw)
		assertOpenRejectsEffective(t, dir, "S1", openTurb, "晚于采样时间", boundary.Add(1), boundary)
	})
}

// 多项目样品只有浊度一项的生效时间晚于采样时间（pH 依据合法、标记一致）：
// 整次打开失败并点名浊度，不能只保留 pH 的结论。
func TestOpenMultiItemOneFutureEffective(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}, {Item: openTurb, Value: 4}},
				Status:       StatusConfirmed, Exceeded: true,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: at(1, 0), Exceeded: true},
					{Item: openTurb, Value: 4, Limit: 5, LimitEffective: at(11, 0), Exceeded: false},
				},
			},
		},
	})
	assertOpenRejectsEffective(t, dir, "S1", openTurb, "晚于采样时间")
}

// 已确认后作废、逐项判定仍保留的样品，生效时间晚于采样时间同样拒绝：
// 作废只取消有效资格，不能让采样之后才生效的上限所作结论借“历史记录”混入。
func TestOpenVoidedAfterConfirmedFutureEffective(t *testing.T) {
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
	assertOpenRejectsEffective(t, dir, "S1", openTurb, "晚于采样时间")
}

// 同一文件中另一份样品完整正常，也不能跳过生效依据不合法的记录继续打开：
// 整次失败、无可用存放，正常样品也读不到。
func TestOpenFutureEffectiveRejectsWholeFile(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S-GOOD": {
				ID: "S-GOOD", PointID: "P1", SampledAt: at(5, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openPH, Value: 7, Limit: 8, LimitEffective: at(1, 0), Exceeded: false},
				},
			},
			"S-BAD": {
				ID: "S-BAD", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openTurb, Value: 4}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openTurb, Value: 4, Limit: 5, LimitEffective: at(11, 0), Exceeded: false},
				},
			},
		},
	})
	assertOpenRejectsEffective(t, dir, "S-BAD", openTurb, "晚于采样时间")
}

// 没有逐项判定的待判定样品，以及待判定后直接作废的样品，不适用生效时间核对：
// 它们本就没有判定依据，照常打开，不为它们补造上限或结论。
func TestOpenPendingAndVoidedFromPendingNoEffectiveCheck(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"SP": {
				ID: "SP", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusPending,
			},
			"SV": {
				ID: "SV", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusVoided, VoidReason: "录入信息有误",
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("没有逐项判定的待判定/直接作废样品应照常打开: %v", err)
	}
	defer s.Close()
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 2 {
		t.Fatalf("两份样品都应读入: %+v err=%v", list, err)
	}
	for _, smp := range list {
		if len(smp.Results) != 0 {
			t.Fatalf("%s 不应被补造判定依据: %+v", smp.ID, smp.Results)
		}
	}
}

// 核对只检查样品自己保存的生效依据，不要求它仍是当前登记限值中最新的一版：
// 后来补录了一版采样之前生效的新上限（7@9/5），保存的旧依据是 100@9/1，
// 只要旧依据的生效时间合法且完整性核对通过，就原样读入旧结论，不切换、不重算。
func TestOpenOldEffectiveBasisNeedNotBeLatestRegistered(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {
				{PointID: "P1", Item: openPH, Value: 100, Effective: at(1, 0)},
				{PointID: "P1", Item: openPH, Value: 7, Effective: at(5, 0)},
			},
		},
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				// 保存的旧依据 100@9/1：9 ≤ 100 达标；若按当前最新的 7@9/5
				// 重算会变成超标，但读入必须原样保留旧结论。
				Status: StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 100, LimitEffective: at(1, 0), Exceeded: false},
				},
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("旧依据生效时间合法时应原样读入，不要求是最新登记版本: %v", err)
	}
	defer s.Close()
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("旧结论应照常作为最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
	r := latest.Results[0]
	if r.Value != 9 || r.Limit != 100 || !r.LimitEffective.Equal(at(1, 0)) || r.Exceeded || latest.Exceeded {
		t.Fatalf("旧上限、生效时间与达标结论必须原样保留: %+v exceeded=%v", r, latest.Exceeded)
	}
}
