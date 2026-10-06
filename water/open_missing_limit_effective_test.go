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
// 文件中实际存在的每一版登记限值都必须写明从什么时候生效，effective 字段缺失、
// 保存为 null 或明确写成 Go 零时间 0001-01-01T00:00:00Z 都视为缺少生效时间——
// JSON 反序列化后三者都是零时间，与 SetLimit 登记时拒绝零时间遵循同一规则，
// 读回来的版本不能例外。即使采样点、项目和上限数值完整，也不能把缺日期的一版
// 解释成很早生效的上限。整次 Open 失败、返回 nil 存放、错误可识别为
// ErrCorruptRecord，并点到该限值自身的采样点编号与项目名、明确说明缺少的是
// 登记限值的生效时间；不补日期、不借用同项目另一版的日期、不跳过这一版或其他
// 正常采样点与样品、不重新计算或改写样品结论、不改写原文件，也不把拒绝推迟到
// 确认某份样品时。核对与当前有没有样品、这一版是否已被样品采用、同项目是否还有
// 其他完整版本无关。日期完整的未来版本不是损坏，照常读入；没有登记限值的数据
// 照常打开，待判定样品缺少适用上限时仍在确认时按原有规则拒绝。

// assertOpenRejectsLimitEffective 断言打开因某版登记限值缺少生效时间而整次失败：
// 返回 nil 存放、错误可识别为 ErrCorruptRecord，信息点到该限值自身的采样点编号
// 与项目名并说明缺少登记限值的生效时间，原文件字节不被改动。
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
		t.Fatalf("缺少生效时间的登记限值必须让整次 Open 失败，%s/%s", point, item)
	}
	if got != nil {
		t.Fatalf("打开失败时不得返回可用的数据存放对象，got %#v", got)
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{point, item, "登记限值", "生效时间", "effective"} {
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

// 任务示例：浊度上限为 5 但没有生效日期，9 月 10 日采样、测量值为 4 的待判定
// 样品不能因此得到达标结论——问题必须在打开数据时就被指出，而不是拖到确认时。
// effective 字段缺失、保存为 null、写成 Go 零时间，三种写法都算缺少生效时间。
func TestOpenLimitMissingEffectiveField(t *testing.T) {
	cases := []struct {
		name           string
		effectiveField string // 直接拼到 "value": 5 之后的内容
	}{
		{"字段缺失", ""},
		{"保存为null", `, "effective": null`},
		{"Go零时间", `, "effective": "0001-01-01T00:00:00Z"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-turb": [{"pointId": "P1", "item": "浊度", "value": 5` +
				// 三种拼法：", \"effective\": null"、", \"effective\": \"零时间\"" 或不加任何内容。
				c.effectiveField + `}]},
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
			// 再开一次仍失败：没有任何内部状态被保留，也没有补写日期。
			if s, err := Open(dir); !errors.Is(err, ErrCorruptRecord) || s != nil {
				t.Fatalf("缺少生效时间必须持续拒绝且返回 nil 存放，s=%v err=%v", s, err)
			}
		})
	}
}

// 经 Go 结构体序列化落盘的零时间同样是损坏：即使数值、采样点、项目齐全也拒绝。
func TestOpenLimitGoZeroEffectiveViaStruct(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openTurb): {
				{PointID: "P1", Item: openTurb, Value: 5}, // Effective 为 Go 零时间
			},
		},
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openTurb, Value: 4}},
				Status:       StatusPending,
			},
		},
	})
	assertOpenRejectsLimitEffective(t, dir, "P1", openTurb)
}

// 文件尚未录入任何样品时，缺少生效时间的限值同样让整次打开失败：
// 核对与有没有样品无关，不能等确认某份样品时才报告。
func TestOpenLimitMissingEffectiveWithoutSamples(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": [{"pointId": "P1", "item": "pH", "value": 8}]}
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsLimitEffective(t, dir, "P1", openPH)
}

// 同一项目有一版日期完整的限值，也不能遮住另一版缺日期的限值：
// 缺日期的一版无论排在完整版本之前还是之后，整次打开都失败。
func TestOpenOneCompleteVersionCannotMaskMissingEffective(t *testing.T) {
	cases := []struct {
		name  string
		order string
	}{
		{"缺日期版在前", "missing-first"},
		{"缺日期版在后", "missing-last"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			complete := `{"pointId": "P1", "item": "pH", "value": 8, "effective": "` +
				at(5, 0).Format(time.RFC3339Nano) + `"}`
			missing := `{"pointId": "P1", "item": "pH", "value": 6}`
			versions := "[" + complete + "," + missing + "]"
			if c.order == "missing-first" {
				versions = "[" + missing + "," + complete + "]"
			}
			raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": ` + versions + `},
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

// 缺日期的限值与其他采样点、其他项目的完整限值以及正常样品同处一个文件：
// 不能跳过坏限值继续打开，整次失败、无可用存放，其余数据也读不到。
func TestOpenLimitMissingEffectiveRejectsWholeFile(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {
    "P1": {"id": "P1", "name": "一号取水口"},
    "P2": {"id": "P2", "name": "二号取水口"}
  },
  "limits": {
    "P1-turb": [{"pointId": "P1", "item": "浊度", "value": 5}],
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
	assertOpenRejectsLimitEffective(t, dir, "P1", openTurb)
	if s, err := Open(dir); !errors.Is(err, ErrCorruptRecord) || s != nil {
		t.Fatalf("其他采样点、限值与样品再正常也不能让整次打开通过，s=%v err=%v", s, err)
	}
}

// 同项目已有完整、合法的历史结论，也不能为缺日期的限值背书：整次打开照样失败，
// 不会部分读入，也不重新计算或改写已保存的样品结论。
func TestOpenLimitMissingEffectiveWithConfirmedSamples(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": [
    {"pointId": "P1", "item": "pH", "value": 8, "effective": "` + at(1, 0).Format(time.RFC3339Nano) + `"},
    {"pointId": "P1", "item": "pH", "value": 6}
  ]},
  "samples": {
    "SC": {
      "id": "SC", "pointId": "P1",
      "sampledAt": "` + at(5, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 9}],
      "status": "confirmed", "exceeded": true,
      "results": [{"item": "pH", "value": 9, "limit": 8, "limitEffective": "` +
		at(1, 0).Format(time.RFC3339Nano) + `", "exceeded": true}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsLimitEffective(t, dir, "P1", openPH)
}

// 日期完整的未来版本不是损坏：即使打开时尚未到生效时刻也正常读入，
// 确认时仍只选采样当时已生效的最近一版；采样时还没有任何适用上限的样品
// 继续按原有规则在确认时拒绝。
func TestOpenFutureLimitWithCompleteDateAccepted(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {
				{PointID: "P1", Item: openPH, Value: 7, Effective: at(1, 0)},
				{PointID: "P1", Item: openPH, Value: 6, Effective: at(20, 0)}, // 打开与采样时都尚未生效
			},
		},
		Samples: map[string]*Sample{
			// 9/6 采样：适用 9/1 生效的 7，7 不超标。
			"S-MID": {
				ID: "S-MID", PointID: "P1", SampledAt: at(6, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusPending,
			},
			// 9/25 采样的样品先不录入；这里只验证未来版本不挡打开。
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("日期完整的未来版本尚未生效也应正常读入: %v", err)
	}
	defer s.Close()
	mid, err := s.Confirm("S-MID")
	if err != nil {
		t.Fatalf("Confirm S-MID: %v", err)
	}
	if r := mid.Results[0]; r.Limit != 7 || !r.LimitEffective.Equal(at(1, 0)) || r.Exceeded {
		t.Fatalf("9/6 采样应取采样前已生效的 7，未来版本不参与: %+v", r)
	}
}

// 只有采样点、没有任何登记限值的数据照常打开；待判定样品缺少适用上限时
// 继续按原有规则在确认时拒绝，不会因为新核对而改变。
func TestOpenNoLimitsAndMissingApplicableLimitUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeRawDiskFile(t, dir, `{"points": {"P1": {"id": "P1", "name": "一号取水口"}}}`)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("没有登记限值的数据应正常打开: %v", err)
	}
	defer s.Close()
	mustSample(t, s, "S1", "P1", at(10, 0), Measurement{Item: openPH, Value: 7})
	if _, err := s.Confirm("S1"); !errors.Is(err, ErrMissingLimit) {
		t.Fatalf("缺少适用上限仍应在确认时拒绝，got %v", err)
	}
}

// 打开因限值缺生效时间失败后，用补齐日期的同一批数据覆盖重写应能正常打开，
// 证明拒绝只发生在读取核对，没有留下任何内部状态或部分数据。
func TestOpenLimitMissingEffectiveRejectionLeavesNoState(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-turb": [{"pointId": "P1", "item": "浊度", "value": 5}]}
}`
	writeRawDiskFile(t, dir, raw)
	if got, err := Open(dir); !errors.Is(err, ErrCorruptRecord) || got != nil {
		t.Fatalf("缺少生效时间必须拒绝并返回 nil 存放, got=%v err=%v", got, err)
	}
	fixed := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-turb": [{"pointId": "P1", "item": "浊度", "value": 5, "effective": "` +
		at(1, 0).Format(time.RFC3339Nano) + `"}]}
}`
	writeRawDiskFile(t, dir, fixed)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("补齐生效时间后应正常打开: %v", err)
	}
	defer s.Close()
	mustSample(t, s, "S1", "P1", at(10, 0), Measurement{Item: openTurb, Value: 4})
	smp, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if r := smp.Results[0]; r.Limit != 5 || !r.LimitEffective.Equal(at(1, 0)) || r.Exceeded {
		t.Fatalf("补齐日期后 4 ≤ 5 应按 9/1 生效的一版判达标: %+v", r)
	}
}
