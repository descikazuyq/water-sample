package water

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件保护“打开本地数据”时对登记限值版本（limits）的数值完整性核对：
// 文件中实际存在的每一版限值都必须实际保存上限数值。value 字段缺失或保存为
// null 时，JSON 反序列化会把它读成 float64 零，与明确保存的零无法区分，缺
// 数值的限值就可能被当成零上限，随后确认采用该版限值的待判定样品时会把测量
// 值同零比较、甚至保存错误的达标结论。出现这种限值时整次 Open 必须失败、
// 返回 nil 存放，错误可被 errors.Is(err, ErrCorruptRecord) 识别，并点到该条
// 限值自身的采样点编号、项目与生效时间、说明缺少上限数值。
//
// 核对针对每一版限值：旧版或尚未生效的版本缺数值也拒绝，不只看最新版本；与
// 有没有样品、该版是否已被样品采用无关，文件没有任何样品时也一样。其他采样
// 点、限值与样品再正常也不部分读入；原文件字节不变；不补成零、不跳过该版、
// 不借用同项目另一版的数值，也不把问题推迟到确认样品时。明确保存数值零的
// 限值仍是合法上限（测量值零达标、大于零超标），负数、正数保持原含义；完整
// 的多版本限值即使保存顺序不同，确认时仍按采样时间选取当时已生效的最近一版。

// assertOpenRejectsMissingLimitValue 断言打开因某条登记限值缺少 value 而整次
// 失败：返回 nil 存放、错误为 ErrCorruptRecord，信息点到采样点、项目与该条
// 限值的生效时间（UTC 的 time.String() 形式），并明确说明缺少上限数值；
// 原文件字节不变。
func assertOpenRejectsMissingLimitValue(t *testing.T, dir, point, item string, eff time.Time) {
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
		t.Fatalf("缺少数值的限值必须让整次 Open 失败，%s/%s @ %s", point, item, eff)
	}
	if got != nil {
		t.Fatalf("打开失败时不得返回可用的数据存放对象，got %#v", got)
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{point, item, eff.UTC().String(), "缺少上限数值", "value"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误信息应包含 %q（采样点/项目/生效时间/缺少上限数值/value 字段），实际 %q", want, msg)
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

// 单版限值的 value 字段整个缺失：不能被读成零上限，整次打开失败并点名采样点、
// 项目与生效时间。
func TestOpenRegisteredLimitMissingValue(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"` + diskLimitKey("P1", openPH) + `": [
    {"pointId": "P1", "item": "` + openPH + `", "effective": "` +
		openEff().Format(time.RFC3339Nano) + `"}
  ]}
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingLimitValue(t, dir, "P1", openPH, openEff())
}

// value 保存为 null 与字段缺失一样：null 不是数值零。
func TestOpenRegisteredLimitNullValue(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"` + diskLimitKey("P1", openPH) + `": [
    {"pointId": "P1", "item": "` + openPH + `", "value": null, "effective": "` +
		openEff().Format(time.RFC3339Nano) + `"}
  ]}
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingLimitValue(t, dir, "P1", openPH, openEff())
}

// 任务场景：缺数值的限值与一份适用该版限值的待判定样品同处一个文件。缺数值
// 会被读成零上限，若放过打开，确认时测量值就会同零比较、甚至保存达标结论；
// 必须在打开阶段整次拒绝，样品也读不到。
func TestOpenRegisteredLimitMissingValueWithPendingSample(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"` + diskLimitKey("P1", openPH) + `": [
    {"pointId": "P1", "item": "` + openPH + `", "effective": "` +
		openEff().Format(time.RFC3339Nano) + `"}
  ]},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "` + openPH + `", "value": 7}],
      "status": "pending"
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingLimitValue(t, dir, "P1", openPH, openEff())
	// 不能因为样品本身完整就跳过限值核对：再次打开仍失败，无法走到 Confirm。
	if _, err := Open(dir); !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("缺数值限值必须让整次打开失败，样品完整也不能放行，got %v", err)
	}
}

// 同一项目有多版限值时，旧版缺数值也要拒绝，即使最新一版数值完整、样品实际
// 会采用最新版：核对覆盖每一版，不只检查最新版本。
func TestOpenRegisteredLimitOldVersionMissingValue(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"` + diskLimitKey("P1", openPH) + `": [
    {"pointId": "P1", "item": "` + openPH + `", "value": 8, "effective": "` +
		at(5, 0).Format(time.RFC3339Nano) + `"},
    {"pointId": "P1", "item": "` + openPH + `", "effective": "` +
		at(1, 0).Format(time.RFC3339Nano) + `"}
  ]},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "` + openPH + `", "value": 9}],
      "status": "pending"
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingLimitValue(t, dir, "P1", openPH, at(1, 0))
}

// 尚未生效的一版缺数值同样拒绝：即使目前没有任何样品会选到它，它仍是文件中
// 实际存在的一版限值，不能等将来生效或确认样品时才暴露。
func TestOpenRegisteredLimitFutureVersionMissingValue(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"` + diskLimitKey("P1", openPH) + `": [
    {"pointId": "P1", "item": "` + openPH + `", "effective": "` +
		at(20, 0).Format(time.RFC3339Nano) + `"},
    {"pointId": "P1", "item": "` + openPH + `", "value": 6, "effective": "` +
		at(1, 0).Format(time.RFC3339Nano) + `"}
  ]},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "` + openPH + `", "value": 7}],
      "status": "pending"
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingLimitValue(t, dir, "P1", openPH, at(20, 0))
}

// 文件尚未录入任何样品，限值缺数值也整次拒绝。
func TestOpenRegisteredLimitMissingValueNoSamples(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"` + diskLimitKey("P1", openPH) + `": [
    {"pointId": "P1", "item": "` + openPH + `", "value": null, "effective": "` +
		openEff().Format(time.RFC3339Nano) + `"}
  ]}
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingLimitValue(t, dir, "P1", openPH, openEff())
}

// 另一采样点限值完整、其他样品正常，也不能只跳过缺数值的这一版部分读入：
// 整次失败、无可用存放，正常记录也读不到。
func TestOpenRegisteredLimitMissingValueRejectsWholeFile(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {
    "P1": {"id": "P1", "name": "一号取水口"},
    "P2": {"id": "P2", "name": "二号取水口"}
  },
  "limits": {
    "` + diskLimitKey("P1", openPH) + `": [
      {"pointId": "P1", "item": "` + openPH + `", "effective": "` +
		openEff().Format(time.RFC3339Nano) + `"}
    ],
    "` + diskLimitKey("P2", openPH) + `": [
      {"pointId": "P2", "item": "` + openPH + `", "value": 8, "effective": "` +
		openEff().Format(time.RFC3339Nano) + `"}
    ]
  },
  "samples": {
    "S-OK": {
      "id": "S-OK", "pointId": "P2",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "` + openPH + `", "value": 7}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "` + openPH + `", "value": 7, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingLimitValue(t, dir, "P1", openPH, openEff())
	if _, err := Open(dir); !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("正常采样点与样品也不能让整次打开通过，got %v", err)
	}
}

// 生效时间用不同时区偏移写成同一时刻，缺 value 仍必须被识别：归属按记录自身
// 的采样点与项目，时刻按真实瞬间比较。
func TestOpenRegisteredLimitMissingValueTimezone(t *testing.T) {
	dir := t.TempDir()
	// 2026-09-01T08:00:00+08:00 与 openEff()（UTC 零点）是同一瞬间。
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"` + diskLimitKey("P1", openPH) + `": [
    {"pointId": "P1", "item": "` + openPH + `", "effective": "2026-09-01T08:00:00+08:00"}
  ]}
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingLimitValue(t, dir, "P1", openPH, openEff())
}

// 缺数值的限值与同一项目的另一版限值同时存在，且放在不同的落盘键下：不能借用
// 另一版的数值填补，也不能跳过缺数值的这一版；归属按记录自身字段重新分组。
func TestOpenRegisteredLimitMissingValueNoBorrowFromOtherVersion(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {
    "` + diskLimitKey("P1", openPH) + `": [
      {"pointId": "P1", "item": "` + openPH + `", "value": 8, "effective": "` +
		at(5, 0).Format(time.RFC3339Nano) + `"}
    ],
    "旧版本占位键": [
      {"pointId": "P1", "item": "` + openPH + `", "effective": "` +
		at(1, 0).Format(time.RFC3339Nano) + `"}
    ]
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingLimitValue(t, dir, "P1", openPH, at(1, 0))
}

// 明确保存数值零的限值是合法上限：适用上限为零时，测量值零应达标、大于零应
// 超标；读入后完成确认，结论按已保存规则核对。
func TestOpenExplicitZeroRegisteredLimitIsValid(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "一号取水口")
	mustLimit(t, s, "P1", openPH, 0, openEff())
	mustSample(t, s, "S0", "P1", at(10, 0), Measurement{Item: openPH, Value: 0})
	mustSample(t, s, "S1", "P1", at(11, 0), Measurement{Item: openPH, Value: 0.5})
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("明确保存的零上限是合法限值，应正常读入: %v", err)
	}
	defer s2.Close()
	conf0, err := s2.Confirm("S0")
	if err != nil || conf0.Exceeded || conf0.Results[0].Limit != 0 {
		t.Fatalf("测量值 0 对零上限应达标: conf=%+v err=%v", conf0, err)
	}
	conf1, err := s2.Confirm("S1")
	if err != nil || !conf1.Exceeded || conf1.Results[0].Limit != 0 {
		t.Fatalf("测量值 0.5 对零上限应超标: conf=%+v err=%v", conf1, err)
	}
}

// 完整的多版本限值即使保存顺序被打乱，正常打开后仍按采样时间选取当时已生效
// 的最近一版：限值核对不改变版本选择规则。
func TestOpenCompleteLimitsOutOfOrderSelectBySamplingTime(t *testing.T) {
	dir := t.TempDir()
	// 故意把较晚生效的版本放在数组前面。
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"` + diskLimitKey("P1", openPH) + `": [
    {"pointId": "P1", "item": "` + openPH + `", "value": 8, "effective": "` +
		at(5, 0).Format(time.RFC3339Nano) + `"},
    {"pointId": "P1", "item": "` + openPH + `", "value": 6, "effective": "` +
		at(1, 0).Format(time.RFC3339Nano) + `"}
  ]},
  "samples": {
    "S-EARLY": {
      "id": "S-EARLY", "pointId": "P1",
      "sampledAt": "` + at(3, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "` + openPH + `", "value": 7}],
      "status": "pending"
    },
    "S-LATE": {
      "id": "S-LATE", "pointId": "P1",
      "sampledAt": "` + at(6, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "` + openPH + `", "value": 7}],
      "status": "pending"
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("数值完整的多版本限值应正常读入: %v", err)
	}
	defer s.Close()
	early, err := s.Confirm("S-EARLY")
	if err != nil || early.Results[0].Limit != 6 || !early.Exceeded {
		t.Fatalf("9 月 3 日应采用 9 月 1 日生效的上限 6，7 > 6 应超标: %+v err=%v", early, err)
	}
	late, err := s.Confirm("S-LATE")
	if err != nil || late.Results[0].Limit != 8 || late.Exceeded {
		t.Fatalf("9 月 6 日应采用 9 月 5 日生效的上限 8，7 <= 8 应达标: %+v err=%v", late, err)
	}
}

// 没有登记限值的空数据照常打开；后续样品找不到适用上限时仍按原有行为以
// ErrMissingLimit 拒绝确认，样品保持待判定。
func TestOpenNoLimitsStillConfirmsMissingLimitError(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "一号取水口")
	mustSample(t, s, "S1", "P1", at(10, 0), Measurement{Item: openPH, Value: 7})
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("没有限值的空数据应正常打开: %v", err)
	}
	defer s2.Close()
	if _, err := s2.Confirm("S1"); !errors.Is(err, ErrMissingLimit) {
		t.Fatalf("缺少适用上限时应返回 ErrMissingLimit，got %v", err)
	}
	list, err := s2.ListByPoint("P1")
	if err != nil || len(list) != 1 || list[0].Status != StatusPending {
		t.Fatalf("确认被拒绝后样品应保持待判定: %+v err=%v", list, err)
	}
}

// 已保存的确认结论与作废记录中的历史依据原样保留：限值缺数值的核对只针对
// limits 中的登记版本，不重选限值、不重新判定；历史结论完整的文件即使后来
// 没有任何适用限值也照常读入。
func TestOpenMissingRegisteredValueKeepsHistoryRule(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "` + openPH + `", "value": 9}],
      "status": "confirmed", "exceeded": true,
      "results": [{"item": "` + openPH + `", "value": 9, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": true}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("没有登记限值、历史判定依据完整的文件应照常读入: %v", err)
	}
	defer s.Close()
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("历史确认结论应原样保留: %+v ok=%v err=%v", latest, ok, err)
	}
	r := latest.Results[0]
	if r.Limit != 8 || !r.LimitEffective.Equal(openEff()) || !r.Exceeded {
		t.Fatalf("已保存的判定依据不应因读取核对而改选或重判: %+v", r)
	}
}
