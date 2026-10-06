package water

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件保护“打开本地数据”时对登记限值生效时间的完整性核对：
// 文件中实际存在的每一版登记限值都必须写明从什么时候生效。effective 字段
// 缺失、保存为 null 或明确写成 Go 零时间 0001-01-01T00:00:00Z，JSON
// 反序列化后都是零时间，一律视为缺少生效时间，不能解释成一版很早以前就生效
// 的上限——即使采样点、项目和上限数值完整也不能放行。整次 Open 失败、返回
// nil 存放、错误可识别为 ErrCorruptRecord，信息点到该限值自身的采样点编号与
// 项目名，并明确说明缺少的是登记限值的生效时间；不补日期、不借用同项目另一
// 版的日期、不跳过坏限值、不重新计算或改写样品结论、不改写原文件，也不把
// 拒绝推迟到确认某份样品时。这条核对与当前有没有样品、这一版是否已被样品
// 采用、同组是否另有完整版本无关。生效时间完整的未来版本照常读入；没有登记
// 限值且其余数据完整的目录照常打开。

// assertOpenRejectsLimitEffective 断言打开因某版登记限值缺少生效时间而整次
// 失败：返回 nil 存放、错误可识别为 ErrCorruptRecord，信息点到该限值自身的
// 采样点编号与项目名并说明缺少登记限值的生效时间，原文件字节不被改动。
func assertOpenRejectsLimitEffective(t *testing.T, dir, point, item string) {
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
		t.Fatalf("缺少生效时间的限值必须让整次 Open 失败，%s/%s", point, item)
	}
	if got != nil {
		t.Fatalf("打开失败时不得返回可用的数据存放对象，got %#v", got)
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{point, item, "生效时间", "effective"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误信息应包含 %q（采样点、项目与缺少登记限值生效时间的说明），实际 %q", want, msg)
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

// 任务示例：浊度上限数值 5 完整，但没有生效日期；9 月 10 日采样、测量值 4
// 的待判定样品不能因这版“不知何时生效”的上限得到达标结论——问题必须在打开
// 数据时就被指出，样品根本没有机会进入确认流程。
func TestOpenLimitMissingEffectiveWithPendingSample(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-turb": [{"pointId": "P1", "item": "浊度", "value": 5}]},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "浊度", "value": 4}],
      "status": "pending"
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsLimitEffective(t, dir, "P1", openTurb)
}

// effective 字段缺失、保存为 null 或明确写成 Go 零时间，三种写法都算缺少
// 生效时间，整次打开失败；直接写原始 JSON，因为 Go 结构体序列化总会带上
// 非指针的时间字段。
func TestOpenLimitMissingEffectiveFieldForms(t *testing.T) {
	cases := []struct {
		name   string
		fields string // 拼进限值记录的完整字段文本
	}{
		{"字段缺失", `"pointId": "P1", "item": "pH", "value": 8`},
		{"保存为null", `"pointId": "P1", "item": "pH", "value": 8, "effective": null`},
		{"Go零时间", `"pointId": "P1", "item": "pH", "value": 8, "effective": "0001-01-01T00:00:00Z"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": [{` + c.fields + `}]}
}`
			writeRawDiskFile(t, dir, raw)
			assertOpenRejectsLimitEffective(t, dir, "P1", openPH)
		})
	}
}

// 文件尚未录入任何样品时，缺少生效时间的限值同样让整次打开失败：
// 这条核对与有没有样品无关，不能等确认某份样品时才报告。
func TestOpenLimitMissingEffectiveWithoutSamples(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": [{"pointId": "P1", "item": "pH", "value": 8}]}
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsLimitEffective(t, dir, "P1", openPH)
}

// 同一项目有多版限值，一版日期完整、另一版缺日期：完整的一版不能遮住缺
// 日期的问题，无论缺日期的是较早版本还是尚未生效的未来版本都整次拒绝，
// 也不能借用另一版的日期补上。
func TestOpenOneVersionMissingEffectiveBesideCompleteVersion(t *testing.T) {
	cases := []struct {
		name    string
		rawJSON string
	}{
		{
			"旧版缺日期",
			`{"pointId": "P1", "item": "pH", "value": 7},
    {"pointId": "P1", "item": "pH", "value": 8, "effective": "` + at(5, 0).Format(time.RFC3339Nano) + `"}`,
		},
		{
			"未来版缺日期",
			`{"pointId": "P1", "item": "pH", "value": 8, "effective": "` + at(1, 0).Format(time.RFC3339Nano) + `"},
    {"pointId": "P1", "item": "pH", "value": 6}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": [
    ` + c.rawJSON + `
  ]},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}],
      "status": "pending"
    }
  }
}`
			writeRawDiskFile(t, dir, raw)
			assertOpenRejectsLimitEffective(t, dir, "P1", openPH)
		})
	}
}

// 缺生效时间的限值与其他采样点、其他项目的完整限值以及完整样品同处一个文件：
// 不能只跳过坏限值继续打开，整次失败、无可用存放，其余数据也读不到。
func TestOpenLimitMissingEffectiveRejectsWholeFile(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {
    "P1": {"id": "P1", "name": "一号取水口"},
    "P2": {"id": "P2", "name": "二号取水口"}
  },
  "limits": {
    "P1-pH": [{"pointId": "P1", "item": "pH", "value": 8}],
    "P2-pH": [{"pointId": "P2", "item": "pH", "value": 8, "effective": "` + openEff().Format(time.RFC3339Nano) + `"}],
    "P1-COD": [{"pointId": "P1", "item": "COD", "value": 30, "effective": "` + openEff().Format(time.RFC3339Nano) + `"}]
  },
  "samples": {
    "S-GOOD": {
      "id": "S-GOOD", "pointId": "P2",
      "sampledAt": "` + at(5, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "value": 7, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsLimitEffective(t, dir, "P1", openPH)
	if _, err := Open(dir); !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("其他采样点、限值与样品再正常也不能让整次打开通过，got %v", err)
	}
}

// 一版限值同时缺少生效时间与上限数值：应先报缺少生效时间，两种残缺都不能
// 互相遮盖；这里固定生效时间核对在前，保证错误信息明确指向缺日期。
func TestOpenLimitMissingEffectiveAndValueReportsEffective(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": [{"pointId": "P1", "item": "pH"}]}
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsLimitEffective(t, dir, "P1", openPH)
}

// 两版限值都缺少生效时间时，它们的零时间虽彼此相等，也不能被版本重复核对
// 抢先报成 ErrDuplicateLimitTime：根本问题是两版都没有写明生效时间，
// 必须按 ErrCorruptRecord 拒绝。
func TestOpenTwoVersionsMissingEffectiveAreCorruptNotDuplicate(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": [
    {"pointId": "P1", "item": "pH", "value": 7},
    {"pointId": "P1", "item": "pH", "value": 8}
  ]}
}`
	writeRawDiskFile(t, dir, raw)
	got, err := Open(dir)
	if got != nil {
		t.Fatalf("打开失败时不得返回可用的数据存放对象，got %#v", got)
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("两版都缺生效时间应报 ErrCorruptRecord，got %v", err)
	}
	if errors.Is(err, ErrDuplicateLimitTime) {
		t.Fatalf("缺生效时间不应被报成重复版本，got %v", err)
	}
}

// 已确认与已确认后作废样品保留自己的历史判定依据，坏限值不借它们“用过”或
// “没用过”的名义放行：只要文件里实际有一版限值缺日期，整次打开照样失败，
// 也不重算或改写任何历史结论。
func TestOpenLimitMissingEffectiveWithConfirmedAndVoidedSamples(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-COD": [{"pointId": "P1", "item": "COD", "value": 30}]},
  "samples": {
    "SC": {
      "id": "SC", "pointId": "P1",
      "sampledAt": "` + at(5, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 9}],
      "status": "confirmed", "exceeded": true,
      "results": [{"item": "pH", "value": 9, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": true}]
    },
    "SV": {
      "id": "SV", "pointId": "P1",
      "sampledAt": "` + at(6, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 9}],
      "status": "voided", "voidReason": "复测确认样品污染", "exceeded": true,
      "results": [{"item": "pH", "value": 9, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": true}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsLimitEffective(t, dir, "P1", "COD")
}

// 打开因限值缺生效时间失败后不留下任何内部状态：给这版限值补上生效时间覆盖
// 写回，同一目录即可正常打开，原有样品照常可见。
func TestOpenLimitMissingEffectiveRejectionLeavesNoState(t *testing.T) {
	dir := t.TempDir()
	bad := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": [{"pointId": "P1", "item": "pH", "value": 8}]},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 9}],
      "status": "pending"
    }
  }
}`
	writeRawDiskFile(t, dir, bad)
	if got, err := Open(dir); !errors.Is(err, ErrCorruptRecord) || got != nil {
		t.Fatalf("缺少生效时间必须拒绝，got=%v err=%v", got, err)
	}
	good := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": [{"pointId": "P1", "item": "pH", "value": 8, "effective": "` +
		openEff().Format(time.RFC3339Nano) + `"}]},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 9}],
      "status": "pending"
    }
  }
}`
	writeRawDiskFile(t, dir, good)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("补回生效时间后应正常打开: %v", err)
	}
	defer s.Close()
	conf, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("补回生效时间后应能正常确认: %v", err)
	}
	if r := conf.Results[0]; r.Limit != 8 || !r.LimitEffective.Equal(openEff()) || !r.Exceeded {
		t.Fatalf("应按补回的限值完成确认: %+v", r)
	}
}

// 生效时间完整的未来版本不是损坏：即使打开时尚未到生效时刻也照常读入，
// 确认时仍只选采样当时已生效的最近一版。
func TestOpenFutureLimitVersionWithCompleteEffectiveAccepted(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {
				{PointID: "P1", Item: openPH, Value: 8, Effective: at(1, 0)},
				{PointID: "P1", Item: openPH, Value: 6, Effective: at(20, 0)},
			},
		},
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusPending,
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("日期完整的未来版本应正常读入，不能因尚未生效当成损坏: %v", err)
	}
	defer s.Close()
	conf, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	// 采样当时未来版本尚未生效，仍取 9/1 的 8，7 ≤ 8 达标。
	if r := conf.Results[0]; r.Limit != 8 || !r.LimitEffective.Equal(at(1, 0)) || r.Exceeded {
		t.Fatalf("确认时应选采样当时已生效的最近一版: %+v", r)
	}
}

// 没有登记限值且其余数据完整的目录照常打开，已有待判定样品缺少适用上限时
// 继续按原规则在确认时拒绝，不受这条限值核对影响。
func TestOpenNoLimitsAndMissingApplicableLimitUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusPending,
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("没有登记限值的完整数据应正常打开: %v", err)
	}
	defer s.Close()
	if _, err := s.Confirm("S1"); !errors.Is(err, ErrMissingLimit) {
		t.Fatalf("缺少适用上限仍应在确认时拒绝，got %v", err)
	}
}
